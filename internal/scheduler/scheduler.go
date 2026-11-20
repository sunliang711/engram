package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"engram/internal/apperror"
	"engram/internal/audit"
	"engram/internal/config"
	"engram/internal/observability"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

const (
	defaultPollInterval = 10 * time.Second
	minTaskInterval     = 10 * time.Second
	maxTaskInterval     = 365 * 24 * time.Hour
)

// Scheduler manages periodic background tasks backed by PostgreSQL state and advisory locks.
type Scheduler struct {
	pool         *pgxpool.Pool
	cfg          config.SchedulerConfig
	auditSvc     *audit.Service
	metrics      *observability.Metrics
	log          zerolog.Logger
	pollInterval time.Duration

	mu     sync.RWMutex
	specs  map[string]TaskSpec
	order  []string
	wakeCh chan struct{}
}

// New creates a Scheduler instance.
func New(pool *pgxpool.Pool, cfg config.SchedulerConfig, auditSvc *audit.Service, metrics *observability.Metrics, log zerolog.Logger) *Scheduler {
	return &Scheduler{
		pool:         pool,
		cfg:          cfg,
		auditSvc:     auditSvc,
		metrics:      metrics,
		log:          log,
		pollInterval: defaultPollInterval,
		specs:        make(map[string]TaskSpec),
		wakeCh:       make(chan struct{}, 1),
	}
}

// SetPollInterval overrides the scheduler tick interval (intended for testing).
func (s *Scheduler) SetPollInterval(d time.Duration) {
	if d > 0 {
		s.pollInterval = d
	}
}

// Register adds a task specification to the scheduler.
func (s *Scheduler) Register(spec TaskSpec) error {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return fmt.Errorf("scheduled task name is required")
	}
	if spec.Run == nil {
		return fmt.Errorf("scheduled task %q missing Run handler", spec.Name)
	}
	if spec.TaskType == "" {
		spec.TaskType = "builtin"
	}
	if spec.DefaultInterval < minTaskInterval {
		spec.DefaultInterval = time.Hour
	}
	if spec.DefaultConfig == nil {
		spec.DefaultConfig = map[string]any{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.specs[spec.Name]; exists {
		return fmt.Errorf("scheduled task %q already registered", spec.Name)
	}
	s.specs[spec.Name] = spec
	s.order = append(s.order, spec.Name)
	return nil
}

// Sync ensures all registered task definitions exist in scheduled_tasks without overwriting runtime config.
func (s *Scheduler) Sync(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return nil
	}
	s.mu.RLock()
	specs := make([]TaskSpec, 0, len(s.order))
	for _, name := range s.order {
		specs = append(specs, s.specs[name])
	}
	s.mu.RUnlock()

	now := time.Now().UTC()
	for _, spec := range specs {
		cfgJSON, err := json.Marshal(spec.DefaultConfig)
		if err != nil {
			return fmt.Errorf("marshal default config for %s: %w", spec.Name, err)
		}
		intervalSec := int64(spec.DefaultInterval.Seconds())
		if intervalSec < 10 {
			intervalSec = 10
		}
		var nextRun *time.Time
		if spec.DefaultEnabled {
			t := now.Add(time.Duration(intervalSec) * time.Second)
			nextRun = &t
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO scheduled_tasks (
				name, display_name, description, task_type, enabled,
				interval_seconds, config_json, next_run_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
			ON CONFLICT (name) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				description = EXCLUDED.description,
				task_type = EXCLUDED.task_type
		`, spec.Name, spec.DisplayName, spec.Description, spec.TaskType, spec.DefaultEnabled, intervalSec, cfgJSON, nextRun, now); err != nil {
			return fmt.Errorf("sync scheduled task %s: %w", spec.Name, err)
		}
	}
	return nil
}

// RunLoop executes due scheduled tasks until ctx is cancelled.
func (s *Scheduler) RunLoop(ctx context.Context) {
	if s == nil || !s.cfg.Enabled || s.pool == nil {
		return
	}
	if err := s.Sync(ctx); err != nil {
		s.log.Error().Err(err).Msg("failed to sync scheduled tasks on startup")
	}

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.RunDue(ctx); err != nil && ctx.Err() == nil {
				s.log.Error().Err(err).Msg("scheduler tick failed")
			}
		case <-s.wakeCh:
			if err := s.RunDue(ctx); err != nil && ctx.Err() == nil {
				s.log.Error().Err(err).Msg("scheduler wakeup run failed")
			}
		}
	}
}

func (s *Scheduler) notifyWakeup() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// RunDue checks for enabled tasks whose next_run_at <= now and runs each one.
func (s *Scheduler) RunDue(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT name
		FROM scheduled_tasks
		WHERE enabled = true AND (next_run_at IS NULL OR next_run_at <= now())
		ORDER BY next_run_at ASC NULLS FIRST, name ASC
	`)
	if err != nil {
		return fmt.Errorf("query due scheduled tasks: %w", err)
	}
	var due []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan due scheduled task: %w", err)
		}
		due = append(due, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate due scheduled tasks: %w", err)
	}

	for _, name := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := s.executeTask(ctx, name, TriggerScheduled, "scheduler"); err != nil {
			if errors.Is(err, errTaskLocked) {
				continue
			}
			s.log.Warn().Err(err).Str("task", name).Msg("scheduled task execution failed")
		}
	}
	return nil
}

var errTaskLocked = errors.New("scheduled task is currently locked by another worker")

// TriggerTask runs a registered scheduled task immediately on demand.
func (s *Scheduler) TriggerTask(ctx context.Context, name, actorName string) (*TaskRun, error) {
	if s == nil || s.pool == nil {
		return nil, apperror.New(apperror.CodeInternalError, "scheduler not initialized")
	}
	name = strings.TrimSpace(name)
	if _, ok := s.getSpec(name); !ok {
		return nil, apperror.New(apperror.CodeNotFound, "scheduled task not found: "+name)
	}
	run, err := s.executeTask(ctx, name, TriggerManual, actorName)
	if errors.Is(err, errTaskLocked) {
		return nil, apperror.New(apperror.CodeVersionConflict, "scheduled task is already running")
	}
	return run, err
}

func (s *Scheduler) getSpec(name string) (TaskSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	spec, ok := s.specs[name]
	return spec, ok
}

func (s *Scheduler) executeTask(ctx context.Context, name, triggerType, actorName string) (*TaskRun, error) {
	spec, ok := s.getSpec(name)
	if !ok {
		return nil, apperror.New(apperror.CodeNotFound, "scheduled task not registered: "+name)
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "acquire connection for scheduled task", err)
	}
	defer conn.Release()

	lockKey := "engram-scheduler:" + name
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, lockKey).Scan(&acquired); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "acquire task advisory lock", err)
	}
	if !acquired {
		return nil, errTaskLocked
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
	}()

	var (
		enabled         bool
		intervalSeconds int64
		cfgRaw          []byte
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT enabled, interval_seconds, config_json
		FROM scheduled_tasks
		WHERE name = $1
	`, name).Scan(&enabled, &intervalSeconds, &cfgRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperror.New(apperror.CodeNotFound, "scheduled task not found: "+name)
		}
		return nil, apperror.Wrap(apperror.CodeInternalError, "load scheduled task config", err)
	}

	cfgMap := maps.Clone(spec.DefaultConfig)
	if cfgMap == nil {
		cfgMap = map[string]any{}
	}
	if len(cfgRaw) > 0 {
		var parsed map[string]any
		if err := json.Unmarshal(cfgRaw, &parsed); err == nil {
			for k, v := range parsed {
				cfgMap[k] = v
			}
		}
	}

	startedAt := time.Now().UTC()
	runID := uuid.New()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO scheduled_task_runs (id, task_name, trigger_type, status, started_at)
		VALUES ($1, $2, $3, $4, $5)
	`, runID, name, triggerType, StatusRunning, startedAt); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "insert scheduled task run", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE scheduled_tasks
		SET last_status = $2, updated_at = $3
		WHERE name = $1
	`, name, StatusRunning, startedAt); err != nil {
		s.log.Warn().Err(err).Str("task", name).Msg("failed to mark scheduled task running")
	}

	res, runErr := spec.Run(ctx, cfgMap)
	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}

	status := StatusSuccess
	errMsg := ""
	if runErr != nil {
		status = StatusFailed
		errMsg = runErr.Error()
		if res.Summary == "" {
			res.Summary = "任务执行失败：" + errMsg
		}
	}
	if res.Details == nil {
		res.Details = map[string]any{}
	}
	detailsJSON, err := json.Marshal(res.Details)
	if err != nil {
		detailsJSON = []byte("{}")
	}

	var nextRun *time.Time
	if enabled && intervalSeconds > 0 {
		t := finishedAt.Add(time.Duration(intervalSeconds) * time.Second)
		nextRun = &t
	}

	dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.pool.Exec(dbCtx, `
		UPDATE scheduled_task_runs
		SET status = $2,
		    finished_at = $3,
		    duration_ms = $4,
		    affected_rows = $5,
		    summary = $6,
		    error_message = $7,
		    details = $8
		WHERE id = $1
	`, runID, status, finishedAt, durationMS, res.AffectedRows, res.Summary, errMsg, detailsJSON); err != nil {
		s.log.Warn().Err(err).Str("task", name).Msg("failed to finalize scheduled_task_runs")
	}

	if _, err := s.pool.Exec(dbCtx, `
		UPDATE scheduled_tasks
		SET last_run_at = $2,
		    next_run_at = $3,
		    last_status = $4,
		    last_duration_ms = $5,
		    last_summary = $6,
		    last_error = $7,
		    run_count = run_count + 1,
		    updated_at = $2
		WHERE name = $1
	`, name, finishedAt, nextRun, status, durationMS, res.Summary, errMsg); err != nil {
		s.log.Warn().Err(err).Str("task", name).Msg("failed to update scheduled_tasks")
	}

	if s.metrics != nil {
		s.metrics.ObserveSchedulerRun(runErr != nil)
	}

	if s.auditSvc != nil {
		src := audit.SourceScheduler
		actorType := audit.ActorScheduler
		if triggerType == TriggerManual {
			src = audit.SourceAdminUI
			actorType = audit.ActorAdmin
		}
		auditDetails := maps.Clone(res.Details)
		if auditDetails == nil {
			auditDetails = map[string]any{}
		}
		auditDetails["trigger_type"] = triggerType
		auditDetails["affected_rows"] = res.AffectedRows
		auditDetails["summary"] = res.Summary

		s.auditSvc.Record(dbCtx, audit.Entry{
			ActorType:    actorType,
			ActorName:    actorName,
			Source:       src,
			Action:       "scheduler.task_run",
			ResourceType: "scheduled_task",
			ResourceID:   name,
			Status:       mapRunStatusToAudit(status),
			ErrorMessage: errMsg,
			DurationMS:   durationMS,
			Details:      auditDetails,
		})
	}

	run := &TaskRun{
		ID:           runID,
		TaskName:     name,
		TriggerType:  triggerType,
		Status:       status,
		StartedAt:    startedAt,
		FinishedAt:   &finishedAt,
		DurationMS:   durationMS,
		AffectedRows: res.AffectedRows,
		Summary:      res.Summary,
		ErrorMessage: errMsg,
		Details:      res.Details,
	}
	if runErr != nil {
		return run, runErr
	}
	return run, nil
}

func mapRunStatusToAudit(runStatus string) string {
	if runStatus == StatusFailed {
		return audit.StatusFailed
	}
	return audit.StatusSuccess
}

// ListTasks returns all scheduled tasks ordered by registration/name.
func (s *Scheduler) ListTasks(ctx context.Context) ([]TaskView, error) {
	if s == nil || s.pool == nil {
		return []TaskView{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT name, display_name, description, task_type, enabled, interval_seconds, config_json,
		       last_run_at, next_run_at, last_status, last_duration_ms, last_summary, last_error,
		       run_count, updated_at
		FROM scheduled_tasks
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "list scheduled tasks", err)
	}
	defer rows.Close()

	items := make([]TaskView, 0)
	for rows.Next() {
		item, err := scanTaskView(rows)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan scheduled task", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "iterate scheduled tasks", err)
	}
	return items, nil
}

// GetTask returns a single scheduled task by name.
func (s *Scheduler) GetTask(ctx context.Context, name string) (*TaskView, error) {
	if s == nil || s.pool == nil {
		return nil, apperror.New(apperror.CodeNotFound, "scheduled task not found")
	}
	row := s.pool.QueryRow(ctx, `
		SELECT name, display_name, description, task_type, enabled, interval_seconds, config_json,
		       last_run_at, next_run_at, last_status, last_duration_ms, last_summary, last_error,
		       run_count, updated_at
		FROM scheduled_tasks
		WHERE name = $1
	`, strings.TrimSpace(name))
	item, err := scanTaskView(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperror.New(apperror.CodeNotFound, "scheduled task not found")
	}
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "get scheduled task", err)
	}
	return &item, nil
}

// UpdateTask validates and persists runtime configuration changes for a scheduled task.
func (s *Scheduler) UpdateTask(ctx context.Context, req UpdateTaskRequest, actorName string) (*TaskView, error) {
	if s == nil || s.pool == nil {
		return nil, apperror.New(apperror.CodeInternalError, "scheduler not initialized")
	}
	name := strings.TrimSpace(req.Name)
	spec, ok := s.getSpec(name)
	if !ok {
		return nil, apperror.New(apperror.CodeNotFound, "scheduled task not found: "+name)
	}

	current, err := s.GetTask(ctx, name)
	if err != nil {
		return nil, err
	}

	enabled := current.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	intervalSeconds := current.IntervalSeconds
	intervalChanged := false
	if strings.TrimSpace(req.Interval) != "" {
		d, err := config.ParseDuration(req.Interval)
		if err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid interval: "+err.Error())
		}
		if d < minTaskInterval || d > maxTaskInterval {
			return nil, apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("interval must be between %s and %s", minTaskInterval, maxTaskInterval))
		}
		intervalSeconds = int64(d.Seconds())
		intervalChanged = true
	} else if req.IntervalSeconds != nil {
		d := time.Duration(*req.IntervalSeconds) * time.Second
		if d < minTaskInterval || d > maxTaskInterval {
			return nil, apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("interval_seconds must be between %d and %d", int64(minTaskInterval.Seconds()), int64(maxTaskInterval.Seconds())))
		}
		intervalSeconds = *req.IntervalSeconds
		intervalChanged = true
	}

	mergedCfg := maps.Clone(current.Config)
	if mergedCfg == nil {
		mergedCfg = map[string]any{}
	}
	for k, v := range req.Config {
		mergedCfg[k] = v
	}
	if spec.ValidateConfig != nil {
		mergedCfg, err = spec.ValidateConfig(mergedCfg)
		if err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, err.Error())
		}
	}
	cfgJSON, err := json.Marshal(mergedCfg)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "marshal scheduled task config", err)
	}

	now := time.Now().UTC()
	var nextRun *time.Time
	if enabled {
		if !current.Enabled || intervalChanged || current.NextRunAt == nil {
			t := now.Add(time.Duration(intervalSeconds) * time.Second)
			nextRun = &t
		} else {
			nextRun = current.NextRunAt
		}
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE scheduled_tasks
		SET enabled = $2,
		    interval_seconds = $3,
		    config_json = $4,
		    next_run_at = $5,
		    updated_at = $6
		WHERE name = $1
	`, name, enabled, intervalSeconds, cfgJSON, nextRun, now); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "update scheduled task", err)
	}

	s.notifyWakeup()

	if s.auditSvc != nil {
		s.auditSvc.Record(ctx, audit.Entry{
			ActorType:    audit.ActorAdmin,
			ActorName:    actorName,
			Source:       audit.SourceAdminUI,
			Action:       "scheduler.task_update",
			ResourceType: "scheduled_task",
			ResourceID:   name,
			Status:       audit.StatusSuccess,
			Details: map[string]any{
				"enabled":          enabled,
				"interval_seconds": intervalSeconds,
				"config":           mergedCfg,
			},
		})
	}

	return s.GetTask(ctx, name)
}

// ListRuns returns paginated execution history for scheduled tasks.
func (s *Scheduler) ListRuns(ctx context.Context, taskName string, page, pageSize int) ([]TaskRun, int, error) {
	if s == nil || s.pool == nil {
		return []TaskRun{}, 0, nil
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	where := "true"
	var args []any
	if strings.TrimSpace(taskName) != "" {
		args = append(args, strings.TrimSpace(taskName))
		where = "task_name = $1"
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::int FROM scheduled_task_runs WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternalError, "count scheduled task runs", err)
	}
	if total == 0 {
		return []TaskRun{}, 0, nil
	}
	maxPage := (total + pageSize - 1) / pageSize
	if page > maxPage {
		page = maxPage
	}
	offset := (page - 1) * pageSize

	queryArgs := append(append([]any(nil), args...), pageSize, offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, task_name, trigger_type, status, started_at, finished_at,
		       duration_ms, affected_rows, summary, error_message, details
		FROM scheduled_task_runs
		WHERE %s
		ORDER BY started_at DESC, id DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternalError, "list scheduled task runs", err)
	}
	defer rows.Close()

	runs := make([]TaskRun, 0, pageSize)
	for rows.Next() {
		var r TaskRun
		var detailsRaw []byte
		if err := rows.Scan(
			&r.ID, &r.TaskName, &r.TriggerType, &r.Status, &r.StartedAt, &r.FinishedAt,
			&r.DurationMS, &r.AffectedRows, &r.Summary, &r.ErrorMessage, &detailsRaw,
		); err != nil {
			return nil, 0, apperror.Wrap(apperror.CodeInternalError, "scan scheduled task run", err)
		}
		if len(detailsRaw) > 0 {
			_ = json.Unmarshal(detailsRaw, &r.Details)
		}
		if r.Details == nil {
			r.Details = map[string]any{}
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternalError, "iterate scheduled task runs", err)
	}
	return runs, total, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTaskView(row rowScanner) (TaskView, error) {
	var t TaskView
	var cfgRaw []byte
	err := row.Scan(
		&t.Name, &t.DisplayName, &t.Description, &t.TaskType, &t.Enabled, &t.IntervalSeconds, &cfgRaw,
		&t.LastRunAt, &t.NextRunAt, &t.LastStatus, &t.LastDurationMS, &t.LastSummary, &t.LastError,
		&t.RunCount, &t.UpdatedAt,
	)
	if err != nil {
		return TaskView{}, err
	}
	t.Interval = (time.Duration(t.IntervalSeconds) * time.Second).String()
	if len(cfgRaw) > 0 {
		_ = json.Unmarshal(cfgRaw, &t.Config)
	}
	if t.Config == nil {
		t.Config = map[string]any{}
	}
	return t, nil
}
