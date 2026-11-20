package audit

import (
	"time"

	"github.com/google/uuid"
)

const (
	SourceMCP       = "mcp"
	SourceAdminUI   = "admin_ui"
	SourceScheduler = "scheduler"
	SourceSystem    = "system"

	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusDenied  = "denied"

	ActorAdmin     = "admin"
	ActorPrincipal = "principal"
	ActorSystem    = "system"
	ActorScheduler = "scheduler"
	ActorAnonymous = "anonymous"

	TaskCredentialHygiene = "credential_hygiene"
	TaskSecurityEvents    = "security_events"
	TaskDataIntegrity     = "data_integrity"
)

// Entry represents a single persisted audit log record.
type Entry struct {
	ID           uuid.UUID      `json:"id"`
	TenantID     *uuid.UUID     `json:"tenant_id,omitempty"`
	ProjectID    *uuid.UUID     `json:"project_id,omitempty"`
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id"`
	ActorName    string         `json:"actor_name"`
	AgentName    string         `json:"agent_name"`
	Source       string         `json:"source"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Status       string         `json:"status"`
	ErrorCode    string         `json:"error_code"`
	ErrorMessage string         `json:"error_message"`
	RequestID    string         `json:"request_id"`
	ClientIP     string         `json:"client_ip"`
	DurationMS   int64          `json:"duration_ms"`
	Details      map[string]any `json:"details"`
	CreatedAt    time.Time      `json:"created_at"`
}

// QueryFilter specifies filters and pagination for listing or exporting audit logs.
type QueryFilter struct {
	TenantID     *uuid.UUID
	Source       string
	Action       string
	Status       string
	ResourceType string
	Query        string
	From         *time.Time
	To           *time.Time
	Page         int
	PageSize     int
}

// QueryResult is a paginated response of audit log entries.
type QueryResult struct {
	Items    []Entry `json:"items"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

// Stats summarizes audit log volume and configuration status.
type Stats struct {
	TotalCount       int64            `json:"total_count"`
	Last24hCount     int64            `json:"last_24h_count"`
	Last24hFailed    int64            `json:"last_24h_failed"`
	OldestAt         *time.Time       `json:"oldest_at,omitempty"`
	NewestAt         *time.Time       `json:"newest_at,omitempty"`
	Enabled          bool             `json:"enabled"`
	LogReads         bool             `json:"log_reads"`
	DefaultRetention string           `json:"default_retention"`
	BySource         map[string]int64 `json:"by_source"`
	ByStatus         map[string]int64 `json:"by_status"`
}

// TaskDefinition describes a built-in audit inspection task.
type TaskDefinition struct {
	TaskType    string `json:"task_type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TenantScope bool   `json:"tenant_scope"`
}

// Finding represents a single finding discovered during an audit task run.
type Finding struct {
	Severity     string         `json:"severity"` // high, medium, low, info
	Code         string         `json:"code"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Count        int64          `json:"count,omitempty"`
	ResourceType string         `json:"resource_type,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

// TaskRun records an execution of an audit inspection task.
type TaskRun struct {
	ID            uuid.UUID  `json:"id"`
	TaskType      string     `json:"task_type"`
	TriggerType   string     `json:"trigger_type"`
	Status        string     `json:"status"`
	TenantID      *uuid.UUID `json:"tenant_id,omitempty"`
	ActorName     string     `json:"actor_name"`
	FindingsCount int        `json:"findings_count"`
	Summary       string     `json:"summary"`
	Findings      []Finding  `json:"findings"`
	ErrorMessage  string     `json:"error_message"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	DurationMS    int64      `json:"duration_ms"`
}
