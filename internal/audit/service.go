package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"engram/internal/apperror"
	"engram/internal/config"
	"engram/internal/observability"
	"engram/internal/security"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

const (
	defaultBatchSize = 1000
	maxBatchSize     = 10000
	maxExportLimit   = 5000
)

// Service provides audit log recording, querying, retention cleanup, and audit inspection tasks.
type Service struct {
	pool    *pgxpool.Pool
	cfg     config.AuditConfig
	metrics *observability.Metrics
	log     zerolog.Logger
}

// NewService creates an Audit Service.
func NewService(pool *pgxpool.Pool, cfg config.AuditConfig, metrics *observability.Metrics, log zerolog.Logger) *Service {
	if cfg.Retention <= 0 {
		cfg.Retention = 30 * 24 * time.Hour
	}
	return &Service{
		pool:    pool,
		cfg:     cfg,
		metrics: metrics,
		log:     log,
	}
}

func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled
}

func (s *Service) LogReads() bool {
	return s != nil && s.cfg.LogReads
}

func (s *Service) DefaultRetention() time.Duration {
	if s == nil || s.cfg.Retention <= 0 {
		return 30 * 24 * time.Hour
	}
	return s.cfg.Retention
}

// Record persists an audit log entry. Failures are logged and never fail the caller's primary operation.
func (s *Service) Record(ctx context.Context, entry Entry) {
	if s == nil || !s.cfg.Enabled || s.pool == nil {
		return
	}
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	entry.Action = strings.TrimSpace(entry.Action)
	if entry.Action == "" {
		entry.Action = "unknown"
	}
	entry.Status = normalizeStatus(entry.Status)
	entry.ActorType = truncateUTF8(strings.TrimSpace(entry.ActorType), 64)
	entry.ActorID = truncateUTF8(strings.TrimSpace(entry.ActorID), 128)
	entry.ActorName = truncateUTF8(strings.TrimSpace(entry.ActorName), 128)
	entry.AgentName = truncateUTF8(strings.TrimSpace(entry.AgentName), 128)
	entry.Source = truncateUTF8(strings.TrimSpace(entry.Source), 64)
	entry.ResourceType = truncateUTF8(strings.TrimSpace(entry.ResourceType), 64)
	entry.ResourceID = truncateUTF8(strings.TrimSpace(entry.ResourceID), 128)
	entry.ErrorCode = truncateUTF8(strings.TrimSpace(entry.ErrorCode), 64)
	entry.ErrorMessage = truncateUTF8(strings.TrimSpace(entry.ErrorMessage), 512)
	entry.RequestID = truncateUTF8(strings.TrimSpace(entry.RequestID), 128)
	entry.ClientIP = truncateUTF8(strings.TrimSpace(entry.ClientIP), 64)
	if entry.DurationMS < 0 {
		entry.DurationMS = 0
	}

	details := SanitizeDetails(entry.Details)
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		detailsJSON = []byte("{}")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	_, err = s.pool.Exec(writeCtx, `
		INSERT INTO audit_logs (
			id, tenant_id, project_id, actor_type, actor_id, actor_name, agent_name,
			source, action, resource_type, resource_id, status, error_code, error_message,
			request_id, client_ip, duration_ms, details, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19
		)
	`,
		entry.ID, entry.TenantID, entry.ProjectID, entry.ActorType, entry.ActorID, entry.ActorName, entry.AgentName,
		entry.Source, entry.Action, entry.ResourceType, entry.ResourceID, entry.Status, entry.ErrorCode, entry.ErrorMessage,
		entry.RequestID, entry.ClientIP, entry.DurationMS, detailsJSON, entry.CreatedAt.UTC(),
	)
	if err != nil {
		s.log.Warn().Err(err).Str("action", entry.Action).Msg("failed to persist audit log")
		return
	}
	if s.metrics != nil {
		s.metrics.IncAuditEvent()
	}
}

func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case StatusFailed:
		return StatusFailed
	case StatusDenied:
		return StatusDenied
	default:
		return StatusSuccess
	}
}

// SanitizeDetails recursively redacts sensitive keys and truncates oversized values before persisting.
func SanitizeDetails(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if isSensitiveKey(k) {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = sanitizeValue(v, 0)
	}
	return out
}

func sanitizeValue(v any, depth int) any {
	if depth > 4 || v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		return truncateUTF8(val, 1024)
	case map[string]any:
		res := make(map[string]any, len(val))
		for k, sub := range val {
			if isSensitiveKey(k) {
				res[k] = "[REDACTED]"
			} else {
				res[k] = sanitizeValue(sub, depth+1)
			}
		}
		return res
	case []any:
		limit := len(val)
		if limit > 50 {
			limit = 50
		}
		res := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			res = append(res, sanitizeValue(val[i], depth+1))
		}
		return res
	case []string:
		limit := len(val)
		if limit > 50 {
			limit = 50
		}
		res := make([]string, 0, limit)
		for i := 0; i < limit; i++ {
			res = append(res, truncateUTF8(val[i], 256))
		}
		return res
	default:
		return v
	}
}

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(strings.TrimSpace(k))
	switch lower {
	case "token_id", "token_prefix", "token_name", "tokens_count", "tokens_resealed", "api_tokens", "active_tokens_count":
		return false
	}
	for _, needle := range []string{"password", "passphrase", "secret", "ciphertext", "raw_token", "sign_key", "authorization", "cookie", "private_key", "project_context"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return lower == "token" || lower == "api_token"
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		if size <= 0 {
			return ""
		}
		s = s[:len(s)-size]
	}
	return s
}

func buildWhereClause(f QueryFilter) (string, []any) {
	var conds []string
	var args []any

	if f.TenantID != nil {
		args = append(args, *f.TenantID)
		conds = append(conds, fmt.Sprintf("tenant_id = $%d", len(args)))
	}
	if s := strings.TrimSpace(f.Source); s != "" {
		args = append(args, s)
		conds = append(conds, fmt.Sprintf("source = $%d", len(args)))
	}
	if a := strings.TrimSpace(f.Action); a != "" {
		args = append(args, "%"+escapeLike(a)+"%")
		conds = append(conds, fmt.Sprintf("action ILIKE $%d ESCAPE '\\'", len(args)))
	}
	if st := strings.TrimSpace(f.Status); st != "" {
		if st == "abnormal" {
			conds = append(conds, "status IN ('failed', 'denied')")
		} else {
			args = append(args, st)
			conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
		}
	}
	if rt := strings.TrimSpace(f.ResourceType); rt != "" {
		args = append(args, rt)
		conds = append(conds, fmt.Sprintf("resource_type = $%d", len(args)))
	}
	if f.From != nil {
		args = append(args, f.From.UTC())
		conds = append(conds, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if f.To != nil {
		args = append(args, f.To.UTC())
		conds = append(conds, fmt.Sprintf("created_at <= $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		p := len(args)
		conds = append(conds, fmt.Sprintf(
			`(action ILIKE $%d ESCAPE '\' OR actor_name ILIKE $%d ESCAPE '\' OR actor_id ILIKE $%d ESCAPE '\' OR agent_name ILIKE $%d ESCAPE '\' OR resource_id ILIKE $%d ESCAPE '\' OR error_message ILIKE $%d ESCAPE '\' OR request_id ILIKE $%d ESCAPE '\')`,
			p, p, p, p, p, p, p,
		))
	}

	if len(conds) == 0 {
		return "true", args
	}
	return strings.Join(conds, " AND "), args
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// QueryLogs returns paginated audit logs matching f.
func (s *Service) QueryLogs(ctx context.Context, f QueryFilter) (*QueryResult, error) {
	if s == nil || s.pool == nil {
		return &QueryResult{Items: []Entry{}, Total: 0, Page: 1, PageSize: 20}, nil
	}
	page := f.Page
	if page <= 0 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	whereSQL, args := buildWhereClause(f)
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::int FROM audit_logs WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "count audit logs", err)
	}
	if total == 0 {
		return &QueryResult{Items: []Entry{}, Total: 0, Page: 1, PageSize: pageSize}, nil
	}
	maxPage := (total + pageSize - 1) / pageSize
	if page > maxPage {
		page = maxPage
	}
	offset := (page - 1) * pageSize

	queryArgs := append(append([]any(nil), args...), pageSize, offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, tenant_id, project_id, actor_type, actor_id, actor_name, agent_name,
		       source, action, resource_type, resource_id, status, error_code, error_message,
		       request_id, client_ip, duration_ms, details, created_at
		FROM audit_logs
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d
	`, whereSQL, len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "query audit logs", err)
	}
	defer rows.Close()

	items := make([]Entry, 0, pageSize)
	for rows.Next() {
		entry, err := scanAuditEntry(rows)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan audit log", err)
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "iterate audit logs", err)
	}
	return &QueryResult{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetLog fetches a single audit log entry by ID.
func (s *Service) GetLog(ctx context.Context, id uuid.UUID) (*Entry, error) {
	if s == nil || s.pool == nil {
		return nil, apperror.New(apperror.CodeNotFound, "audit log not found")
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, project_id, actor_type, actor_id, actor_name, agent_name,
		       source, action, resource_type, resource_id, status, error_code, error_message,
		       request_id, client_ip, duration_ms, details, created_at
		FROM audit_logs
		WHERE id = $1
	`, id)
	entry, err := scanAuditEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperror.New(apperror.CodeNotFound, "audit log not found")
	}
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "get audit log", err)
	}
	return &entry, nil
}

// ExportLogs returns up to limit matching audit logs ordered newest-first.
func (s *Service) ExportLogs(ctx context.Context, f QueryFilter, limit int) ([]Entry, error) {
	if s == nil || s.pool == nil {
		return []Entry{}, nil
	}
	if limit <= 0 || limit > maxExportLimit {
		limit = maxExportLimit
	}
	whereSQL, args := buildWhereClause(f)
	queryArgs := append(append([]any(nil), args...), limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, tenant_id, project_id, actor_type, actor_id, actor_name, agent_name,
		       source, action, resource_type, resource_id, status, error_code, error_message,
		       request_id, client_ip, duration_ms, details, created_at
		FROM audit_logs
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d
	`, whereSQL, len(args)+1), queryArgs...)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "export audit logs", err)
	}
	defer rows.Close()

	items := make([]Entry, 0)
	for rows.Next() {
		entry, err := scanAuditEntry(rows)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan exported audit log", err)
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "iterate exported audit logs", err)
	}
	return items, nil
}

// GetStats returns aggregate volume and time-range statistics for audit_logs.
func (s *Service) GetStats(ctx context.Context) (*Stats, error) {
	st := &Stats{
		Enabled:          s.Enabled(),
		LogReads:         s.LogReads(),
		DefaultRetention: s.DefaultRetention().String(),
		BySource:         map[string]int64{},
		ByStatus:         map[string]int64{},
	}
	if s == nil || s.pool == nil {
		return st, nil
	}
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*)::bigint,
			count(*) FILTER (WHERE created_at >= now() - interval '24 hours')::bigint,
			count(*) FILTER (WHERE created_at >= now() - interval '24 hours' AND status <> 'success')::bigint,
			min(created_at),
			max(created_at)
		FROM audit_logs
	`).Scan(&st.TotalCount, &st.Last24hCount, &st.Last24hFailed, &st.OldestAt, &st.NewestAt)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "query audit stats", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT source, count(*)::bigint FROM audit_logs GROUP BY source`)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "query audit stats by source", err)
	}
	for rows.Next() {
		var src string
		var cnt int64
		if err := rows.Scan(&src, &cnt); err != nil {
			rows.Close()
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan audit source stat", err)
		}
		st.BySource[src] = cnt
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT status, count(*)::bigint FROM audit_logs GROUP BY status`)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "query audit stats by status", err)
	}
	for rows.Next() {
		var status string
		var cnt int64
		if err := rows.Scan(&status, &cnt); err != nil {
			rows.Close()
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan audit status stat", err)
		}
		st.ByStatus[status] = cnt
	}
	rows.Close()

	return st, nil
}

// CleanupExpired deletes audit logs older than retention in bounded batches and returns deleted count and cutoff.
func (s *Service) CleanupExpired(ctx context.Context, retention time.Duration, batchSize int) (int64, time.Time, error) {
	if retention <= 0 {
		retention = s.DefaultRetention()
	}
	cutoff := time.Now().UTC().Add(-retention)
	deleted, err := s.CleanupBefore(ctx, cutoff, batchSize)
	return deleted, cutoff, err
}

// CleanupBefore deletes audit logs created strictly before cutoff in bounded batches.
func (s *Service) CleanupBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	if batchSize > maxBatchSize {
		batchSize = maxBatchSize
	}

	var totalDeleted int64
	for {
		if err := ctx.Err(); err != nil {
			return totalDeleted, err
		}
		tag, err := s.pool.Exec(ctx, `
			WITH doomed AS (
				SELECT id
				FROM audit_logs
				WHERE created_at < $1
				ORDER BY created_at ASC, id ASC
				LIMIT $2
			)
			DELETE FROM audit_logs a
			USING doomed d
			WHERE a.id = d.id
		`, cutoff.UTC(), batchSize)
		if err != nil {
			return totalDeleted, apperror.Wrap(apperror.CodeInternalError, "delete expired audit logs", err)
		}
		n := tag.RowsAffected()
		totalDeleted += n
		if s.metrics != nil && n > 0 {
			s.metrics.AddAuditCleanupDeleted(n)
		}
		if n < int64(batchSize) {
			break
		}
	}
	return totalDeleted, nil
}

// ListTaskDefinitions returns the built-in audit inspection tasks.
func (s *Service) ListTaskDefinitions() []TaskDefinition {
	return []TaskDefinition{
		{
			TaskType:    TaskCredentialHygiene,
			Name:        "凭据与访问合规审计",
			Description: "检查已过期但未吊销的客户端密钥、长期未使用密钥、停用账号名下的有效密钥及孤立成员。",
			TenantScope: true,
		},
		{
			TaskType:    TaskSecurityEvents,
			Name:        "安全事件与异常操作审计",
			Description: "汇总近 7 天管理后台登录失败、MCP 越权/拒绝访问、批量清空及密钥明文查看等敏感事件。",
			TenantScope: true,
		},
		{
			TaskType:    TaskDataIntegrity,
			Name:        "知识库数据与治理完整性审计",
			Description: "核查活跃知识的内容哈希一致性、回收站积压条目、过期幂等记录与未绑定仓库的项目。",
			TenantScope: true,
		},
	}
}

// RunTask executes a built-in audit inspection task and persists its run record and findings.
func (s *Service) RunTask(ctx context.Context, taskType string, tenantID *uuid.UUID, actorName, triggerType string) (*TaskRun, error) {
	if s == nil || s.pool == nil {
		return nil, apperror.New(apperror.CodeInternalError, "audit service not initialized")
	}
	taskType = strings.TrimSpace(taskType)
	if taskType != TaskCredentialHygiene && taskType != TaskSecurityEvents && taskType != TaskDataIntegrity {
		return nil, apperror.New(apperror.CodeInvalidArgument, "unknown audit task_type: "+taskType)
	}
	if triggerType == "" {
		triggerType = "manual"
	}

	startedAt := time.Now().UTC()
	runID := uuid.New()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO audit_task_runs (id, task_type, trigger_type, status, tenant_id, actor_name, started_at)
		VALUES ($1, $2, $3, 'running', $4, $5, $6)
	`, runID, taskType, triggerType, tenantID, actorName, startedAt); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "create audit task run", err)
	}

	var (
		findings []Finding
		summary  string
		runErr   error
	)
	switch taskType {
	case TaskCredentialHygiene:
		findings, summary, runErr = s.runCredentialHygieneTask(ctx, tenantID)
	case TaskSecurityEvents:
		findings, summary, runErr = s.runSecurityEventsTask(ctx, tenantID)
	case TaskDataIntegrity:
		findings, summary, runErr = s.runDataIntegrityTask(ctx, tenantID)
	}

	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	status := "success"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		errMsg = truncateUTF8(runErr.Error(), 512)
		if findings == nil {
			findings = []Finding{}
		}
	}
	if findings == nil {
		findings = []Finding{}
	}
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		findingsJSON = []byte("[]")
	}

	if _, err := s.pool.Exec(ctx, `
		UPDATE audit_task_runs
		SET status = $2,
		    findings_count = $3,
		    summary = $4,
		    findings = $5,
		    error_message = $6,
		    finished_at = $7,
		    duration_ms = $8
		WHERE id = $1
	`, runID, status, len(findings), summary, findingsJSON, errMsg, finishedAt, durationMS); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "finalize audit task run", err)
	}

	if runErr != nil {
		return nil, runErr
	}

	return &TaskRun{
		ID:            runID,
		TaskType:      taskType,
		TriggerType:   triggerType,
		Status:        status,
		TenantID:      tenantID,
		ActorName:     actorName,
		FindingsCount: len(findings),
		Summary:       summary,
		Findings:      findings,
		ErrorMessage:  errMsg,
		StartedAt:     startedAt,
		FinishedAt:    &finishedAt,
		DurationMS:    durationMS,
	}, nil
}

func (s *Service) runCredentialHygieneTask(ctx context.Context, tenantID *uuid.UUID) ([]Finding, string, error) {
	findings := make([]Finding, 0)
	var expiredUnrevoked, staleUnused, disabledWithTokens, orphanPrincipals int64

	if tenantID != nil {
		err := s.pool.QueryRow(ctx, `
			SELECT
				count(*) FILTER (WHERE t.revoked_at IS NULL AND t.expires_at IS NOT NULL AND t.expires_at < now())::bigint,
				count(*) FILTER (WHERE t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at >= now()) AND COALESCE(t.last_used_at, t.created_at) < now() - interval '90 days')::bigint,
				count(*) FILTER (WHERE t.revoked_at IS NULL AND (p.status <> 'active' OR tn.status <> 'active'))::bigint
			FROM api_tokens t
			JOIN principals p ON p.id = t.principal_id
			JOIN tenants tn ON tn.id = t.tenant_id
			WHERE t.tenant_id = $1
		`, *tenantID).Scan(&expiredUnrevoked, &staleUnused, &disabledWithTokens)
		if err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "audit credential hygiene", err)
		}
	} else {
		err := s.pool.QueryRow(ctx, `
			SELECT
				count(*) FILTER (WHERE t.revoked_at IS NULL AND t.expires_at IS NOT NULL AND t.expires_at < now())::bigint,
				count(*) FILTER (WHERE t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at >= now()) AND COALESCE(t.last_used_at, t.created_at) < now() - interval '90 days')::bigint,
				count(*) FILTER (WHERE t.revoked_at IS NULL AND (p.status <> 'active' OR tn.status <> 'active'))::bigint
			FROM api_tokens t
			JOIN principals p ON p.id = t.principal_id
			JOIN tenants tn ON tn.id = t.tenant_id
		`).Scan(&expiredUnrevoked, &staleUnused, &disabledWithTokens)
		if err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "audit credential hygiene", err)
		}
		err = s.pool.QueryRow(ctx, `
			SELECT count(*)::bigint
			FROM principals p
			WHERE p.identity_provider <> 'system_webui'
			  AND NOT EXISTS (SELECT 1 FROM tenant_members tm WHERE tm.principal_id = p.id)
		`).Scan(&orphanPrincipals)
		if err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "audit orphan principals", err)
		}
	}

	if expiredUnrevoked > 0 {
		findings = append(findings, Finding{
			Severity:     "medium",
			Code:         "EXPIRED_UNREVOKED_TOKENS",
			Title:        "存在已过期但未吊销的客户端密钥",
			Description:  fmt.Sprintf("发现 %d 个已过期但尚未标记吊销的客户端密钥，建议及时吊销清理。", expiredUnrevoked),
			Count:        expiredUnrevoked,
			ResourceType: "api_token",
		})
	}
	if staleUnused > 0 {
		findings = append(findings, Finding{
			Severity:     "low",
			Code:         "STALE_UNUSED_TOKENS",
			Title:        "存在超过 90 天未使用的活跃密钥",
			Description:  fmt.Sprintf("发现 %d 个超过 90 天未使用的未吊销密钥，建议核查是否仍需保留。", staleUnused),
			Count:        staleUnused,
			ResourceType: "api_token",
		})
	}
	if disabledWithTokens > 0 {
		findings = append(findings, Finding{
			Severity:     "high",
			Code:         "DISABLED_ENTITY_ACTIVE_TOKENS",
			Title:        "停用成员或租户仍关联未吊销密钥",
			Description:  fmt.Sprintf("发现 %d 个归属于停用成员或停用租户的未吊销密钥，建议立即吊销。", disabledWithTokens),
			Count:        disabledWithTokens,
			ResourceType: "api_token",
		})
	}
	if orphanPrincipals > 0 {
		findings = append(findings, Finding{
			Severity:     "info",
			Code:         "ORPHAN_PRINCIPALS",
			Title:        "存在未归属任何租户的成员账号",
			Description:  fmt.Sprintf("发现 %d 个未加入任何租户的成员记录。", orphanPrincipals),
			Count:        orphanPrincipals,
			ResourceType: "principal",
		})
	}

	if len(findings) == 0 {
		return findings, "凭据与访问合规巡检完成：未发现异常风险项。", nil
	}
	return findings, fmt.Sprintf("凭据与访问合规巡检完成：发现 %d 类关注项。", len(findings)), nil
}

func (s *Service) runSecurityEventsTask(ctx context.Context, tenantID *uuid.UUID) ([]Finding, string, error) {
	findings := make([]Finding, 0)
	var failedLogins, deniedMCP, destructiveOps, tokenReveals int64

	whereTenant := ""
	var args []any
	if tenantID != nil {
		args = append(args, *tenantID)
		whereTenant = " AND (tenant_id = $1 OR tenant_id IS NULL)"
	}

	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE action = 'admin.login' AND status <> 'success')::bigint,
			count(*) FILTER (WHERE source = 'mcp' AND status = 'denied')::bigint,
			count(*) FILTER (WHERE action IN ('memory.purge', 'tenant.delete', 'principal.delete', 'migration.import') AND status = 'success')::bigint,
			count(*) FILTER (WHERE action = 'token.reveal' AND status = 'success')::bigint
		FROM audit_logs
		WHERE created_at >= now() - interval '7 days'`+whereTenant, args...).
		Scan(&failedLogins, &deniedMCP, &destructiveOps, &tokenReveals)
	if err != nil {
		return nil, "", apperror.Wrap(apperror.CodeInternalError, "audit security events", err)
	}

	if failedLogins > 0 {
		sev := "medium"
		if failedLogins >= 10 {
			sev = "high"
		}
		findings = append(findings, Finding{
			Severity:     sev,
			Code:         "ADMIN_LOGIN_FAILURES",
			Title:        "近 7 天存在管理控制台登录失败记录",
			Description:  fmt.Sprintf("近 7 天共记录 %d 次管理员登录失败或限流事件。", failedLogins),
			Count:        failedLogins,
			ResourceType: "admin_session",
		})
	}
	if deniedMCP > 0 {
		findings = append(findings, Finding{
			Severity:     "medium",
			Code:         "MCP_ACCESS_DENIED",
			Title:        "近 7 天存在 MCP 拒绝访问事件",
			Description:  fmt.Sprintf("近 7 天共有 %d 次 MCP 认证失败或越权访问被拒绝。", deniedMCP),
			Count:        deniedMCP,
			ResourceType: "mcp",
		})
	}
	if destructiveOps > 0 {
		findings = append(findings, Finding{
			Severity:     "info",
			Code:         "DESTRUCTIVE_OPERATIONS",
			Title:        "近 7 天执行过高危数据变更操作",
			Description:  fmt.Sprintf("近 7 天共执行 %d 次批量清空、租户/成员删除或系统还原导入操作。", destructiveOps),
			Count:        destructiveOps,
			ResourceType: "system",
		})
	}
	if tokenReveals > 0 {
		findings = append(findings, Finding{
			Severity:     "info",
			Code:         "TOKEN_PLAINTEXT_REVEALS",
			Title:        "近 7 天存在客户端密钥明文查看记录",
			Description:  fmt.Sprintf("近 7 天共有 %d 次在管理控制台读取客户端密钥明文。", tokenReveals),
			Count:        tokenReveals,
			ResourceType: "api_token",
		})
	}

	if len(findings) == 0 {
		return findings, "安全事件审计完成（近 7 天）：未发现异常登录、越权拒绝或高危变更。", nil
	}
	return findings, fmt.Sprintf("安全事件审计完成（近 7 天）：汇总 %d 类安全关注事件。", len(findings)), nil
}

func (s *Service) runDataIntegrityTask(ctx context.Context, tenantID *uuid.UUID) ([]Finding, string, error) {
	findings := make([]Finding, 0)

	var hashMismatches int64
	memQuery := `SELECT content_md, content_hash FROM memories WHERE deleted_at IS NULL`
	var memArgs []any
	if tenantID != nil {
		memArgs = append(memArgs, *tenantID)
		memQuery += ` AND tenant_id = $1`
	}
	rows, err := s.pool.Query(ctx, memQuery, memArgs...)
	if err != nil {
		return nil, "", apperror.Wrap(apperror.CodeInternalError, "audit memory content hashes", err)
	}
	var checkedMemories int64
	for rows.Next() {
		var contentMD, storedHash string
		if err := rows.Scan(&contentMD, &storedHash); err != nil {
			rows.Close()
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "scan memory hash row", err)
		}
		checkedMemories++
		if storedHash != security.ContentHash(contentMD) {
			hashMismatches++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", apperror.Wrap(apperror.CodeInternalError, "iterate memory hash rows", err)
	}
	rows.Close()

	var recycledCount, expiredIdempotency, projectsWithoutRemotes int64
	if tenantID != nil {
		if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM memories WHERE tenant_id = $1 AND deleted_at IS NOT NULL`, *tenantID).Scan(&recycledCount); err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "count recycled memories", err)
		}
		if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM idempotency_requests WHERE tenant_id = $1 AND expires_at < now()`, *tenantID).Scan(&expiredIdempotency); err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "count expired idempotency", err)
		}
		if err := s.pool.QueryRow(ctx, `
			SELECT count(*)::bigint FROM projects p
			WHERE p.tenant_id = $1 AND p.status = 'active' AND p.marker IS NULL
			  AND NOT EXISTS (SELECT 1 FROM project_repositories r WHERE r.project_id = p.id)
		`, *tenantID).Scan(&projectsWithoutRemotes); err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "count unbound projects", err)
		}
	} else {
		if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM memories WHERE deleted_at IS NOT NULL`).Scan(&recycledCount); err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "count recycled memories", err)
		}
		if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM idempotency_requests WHERE expires_at < now()`).Scan(&expiredIdempotency); err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "count expired idempotency", err)
		}
		if err := s.pool.QueryRow(ctx, `
			SELECT count(*)::bigint FROM projects p
			WHERE p.status = 'active' AND p.marker IS NULL
			  AND NOT EXISTS (SELECT 1 FROM project_repositories r WHERE r.project_id = p.id)
		`).Scan(&projectsWithoutRemotes); err != nil {
			return nil, "", apperror.Wrap(apperror.CodeInternalError, "count unbound projects", err)
		}
	}

	if hashMismatches > 0 {
		findings = append(findings, Finding{
			Severity:     "high",
			Code:         "MEMORY_HASH_MISMATCH",
			Title:        "活跃知识存在内容哈希不一致",
			Description:  fmt.Sprintf("在校验的 %d 条活跃知识中，发现 %d 条记录的 content_hash 与正文不一致。", checkedMemories, hashMismatches),
			Count:        hashMismatches,
			ResourceType: "memory",
		})
	}
	if recycledCount > 0 {
		findings = append(findings, Finding{
			Severity:     "info",
			Code:         "RECYCLE_BIN_BACKLOG",
			Title:        "回收站存在待清理的已删除知识",
			Description:  fmt.Sprintf("回收站当前包含 %d 条已软删除知识，可按需在知识库管理中恢复或彻底清理。", recycledCount),
			Count:        recycledCount,
			ResourceType: "memory",
		})
	}
	if expiredIdempotency > 0 {
		findings = append(findings, Finding{
			Severity:     "low",
			Code:         "EXPIRED_IDEMPOTENCY_KEYS",
			Title:        "存在已过期的幂等请求缓存记录",
			Description:  fmt.Sprintf("发现 %d 条已超过 TTL 的幂等请求记录。", expiredIdempotency),
			Count:        expiredIdempotency,
			ResourceType: "idempotency_request",
		})
	}
	if projectsWithoutRemotes > 0 {
		findings = append(findings, Finding{
			Severity:     "low",
			Code:         "PROJECTS_WITHOUT_IDENTITY",
			Title:        "存在既无 Marker 又无 Git 仓库绑定的活跃项目",
			Description:  fmt.Sprintf("发现 %d 个活跃项目缺少 Marker 和仓库地址，客户端可能无法自动识别。", projectsWithoutRemotes),
			Count:        projectsWithoutRemotes,
			ResourceType: "project",
		})
	}

	if len(findings) == 0 {
		return findings, fmt.Sprintf("数据完整性审计完成：已核验 %d 条活跃知识，未发现异常。", checkedMemories), nil
	}
	return findings, fmt.Sprintf("数据完整性审计完成：已核验 %d 条活跃知识，发现 %d 类关注项。", checkedMemories, len(findings)), nil
}

// ListTaskRuns returns paginated audit task run records.
func (s *Service) ListTaskRuns(ctx context.Context, taskType string, page, pageSize int) ([]TaskRun, int, error) {
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
	if strings.TrimSpace(taskType) != "" {
		args = append(args, strings.TrimSpace(taskType))
		where = "task_type = $1"
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::int FROM audit_task_runs WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternalError, "count audit task runs", err)
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
		SELECT id, task_type, trigger_type, status, tenant_id, actor_name, findings_count,
		       summary, findings, error_message, started_at, finished_at, duration_ms
		FROM audit_task_runs
		WHERE %s
		ORDER BY started_at DESC, id DESC
		LIMIT $%d OFFSET $%d
	`, where, len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternalError, "list audit task runs", err)
	}
	defer rows.Close()

	runs := make([]TaskRun, 0, pageSize)
	for rows.Next() {
		var r TaskRun
		var findingsRaw []byte
		if err := rows.Scan(
			&r.ID, &r.TaskType, &r.TriggerType, &r.Status, &r.TenantID, &r.ActorName, &r.FindingsCount,
			&r.Summary, &findingsRaw, &r.ErrorMessage, &r.StartedAt, &r.FinishedAt, &r.DurationMS,
		); err != nil {
			return nil, 0, apperror.Wrap(apperror.CodeInternalError, "scan audit task run", err)
		}
		if len(findingsRaw) > 0 {
			_ = json.Unmarshal(findingsRaw, &r.Findings)
		}
		if r.Findings == nil {
			r.Findings = []Finding{}
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, apperror.Wrap(apperror.CodeInternalError, "iterate audit task runs", err)
	}
	return runs, total, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAuditEntry(row rowScanner) (Entry, error) {
	var e Entry
	var detailsRaw []byte
	err := row.Scan(
		&e.ID, &e.TenantID, &e.ProjectID, &e.ActorType, &e.ActorID, &e.ActorName, &e.AgentName,
		&e.Source, &e.Action, &e.ResourceType, &e.ResourceID, &e.Status, &e.ErrorCode, &e.ErrorMessage,
		&e.RequestID, &e.ClientIP, &e.DurationMS, &detailsRaw, &e.CreatedAt,
	)
	if err != nil {
		return Entry{}, err
	}
	if len(detailsRaw) > 0 {
		_ = json.Unmarshal(detailsRaw, &e.Details)
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	return e, nil
}
