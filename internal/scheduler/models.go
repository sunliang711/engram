package scheduler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	TaskAuditLogCleanup = "audit_log_cleanup"

	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"

	StatusIdle    = "idle"
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusTimeout = "timeout"
)

// TaskResult captures the outcome of a single scheduled task execution.
type TaskResult struct {
	AffectedRows int64          `json:"affected_rows"`
	Summary      string         `json:"summary"`
	Details      map[string]any `json:"details,omitempty"`
}

// TaskSpec defines a runnable scheduled task registered with the Scheduler.
type TaskSpec struct {
	Name            string
	DisplayName     string
	Description     string
	TaskType        string
	DefaultEnabled  bool
	DefaultInterval time.Duration
	DefaultConfig   map[string]any
	ValidateConfig  func(cfg map[string]any) (map[string]any, error)
	Run             func(ctx context.Context, cfg map[string]any) (TaskResult, error)
}

// TaskView is the API representation of a scheduled task and its current state.
type TaskView struct {
	Name            string         `json:"name"`
	DisplayName     string         `json:"display_name"`
	Description     string         `json:"description"`
	TaskType        string         `json:"task_type"`
	Enabled         bool           `json:"enabled"`
	IntervalSeconds int64          `json:"interval_seconds"`
	Interval        string         `json:"interval"`
	ScheduleType    string         `json:"schedule_type"`
	CronExpression  string         `json:"cron_expression"`
	Timezone        string         `json:"timezone"`
	TimeoutSeconds  int64          `json:"timeout_seconds"`
	Config          map[string]any `json:"config"`
	LastRunAt       *time.Time     `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time     `json:"next_run_at,omitempty"`
	LastStatus      string         `json:"last_status"`
	LastDurationMS  int64          `json:"last_duration_ms"`
	LastSummary     string         `json:"last_summary"`
	LastError       string         `json:"last_error"`
	RunCount        int64          `json:"run_count"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// TaskRun records a single execution attempt of a scheduled task.
type TaskRun struct {
	ID           uuid.UUID      `json:"id"`
	TaskName     string         `json:"task_name"`
	TriggerType  string         `json:"trigger_type"`
	Status       string         `json:"status"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   *time.Time     `json:"finished_at,omitempty"`
	DurationMS   int64          `json:"duration_ms"`
	AffectedRows int64          `json:"affected_rows"`
	Summary      string         `json:"summary"`
	ErrorMessage string         `json:"error_message"`
	Details      map[string]any `json:"details"`
}

// UpdateTaskRequest updates a scheduled task's enabled state, interval, or JSON config.
type UpdateTaskRequest struct {
	Name            string         `json:"name"`
	Enabled         *bool          `json:"enabled,omitempty"`
	Interval        string         `json:"interval,omitempty"`
	IntervalSeconds *int64         `json:"interval_seconds,omitempty"`
	ScheduleType    *string        `json:"schedule_type,omitempty"`
	CronExpression  *string        `json:"cron_expression,omitempty"`
	Timezone        *string        `json:"timezone,omitempty"`
	TimeoutSeconds  *int64         `json:"timeout_seconds,omitempty"`
	Config          map[string]any `json:"config,omitempty"`
}
