package memory

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"engram/internal/apperror"
	"engram/internal/project"
	"engram/internal/security"

	"github.com/google/uuid"
)

// DecodeStrictJSON decodes JSON while rejecting any unknown fields (Section 12).
func DecodeStrictJSON(data []byte, dst any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument, "invalid arguments: "+err.Error(), err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return apperror.New(apperror.CodeInvalidArgument, "unexpected trailing data in json payload")
	}
	return nil
}

type PermissionsInfo struct {
	TenantRole         string `json:"tenant_role"`
	ProjectRole        string `json:"project_role,omitempty"`
	CanReadGlobal      bool   `json:"can_read_global"`
	CanWriteGlobal     bool   `json:"can_write_global"`
	CanReadProject     bool   `json:"can_read_project"`
	CanWriteProject    bool   `json:"can_write_project"`
	CanMaintainProject bool   `json:"can_maintain_project"`
}

type IndexItem struct {
	ID        uuid.UUID `json:"id"`
	Category  string    `json:"category"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MemorySummary struct {
	ID        uuid.UUID `json:"id"`
	Scope     string    `json:"scope"`
	Category  string    `json:"category"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Tags      []string  `json:"tags,omitempty"`
	IsPinned  bool      `json:"is_pinned,omitempty"`
	Status    string    `json:"status"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MemoryDetail struct {
	ID             uuid.UUID            `json:"id"`
	TenantID       uuid.UUID            `json:"tenant_id"`
	Scope          string               `json:"scope"`
	ProjectID      *uuid.UUID           `json:"project_id,omitempty"`
	Category       string               `json:"category"`
	Title          string               `json:"title"`
	Content        string               `json:"content"`
	Tags           []string             `json:"tags"`
	SourceRefs     []security.SourceRef `json:"source_refs"`
	IsPinned       bool                 `json:"is_pinned"`
	Status         string               `json:"status"`
	Version        int64                `json:"version"`
	SourceMemoryID *uuid.UUID           `json:"source_memory_id,omitempty"`
	ContentHash    string               `json:"content_hash"`
	CreatedBy      uuid.UUID            `json:"created_by"`
	UpdatedBy      uuid.UUID            `json:"updated_by"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
	DeletedAt      *time.Time           `json:"deleted_at,omitempty"`
}

type ScopeIndex struct {
	Pinned        []IndexItem    `json:"pinned"`
	CategoryStats map[string]int `json:"category_stats"`
	Recent        []IndexItem    `json:"recent"`
}

// 7.1 memory_discover
type DiscoverRequest struct {
	ProjectMarker   string                `json:"project_marker"`
	MarkerPath      string                `json:"marker_path"`
	Remotes         []project.RemoteInput `json:"remotes"`
	WorkspacePath   string                `json:"workspace_path"`
	AllowGlobalOnly bool                  `json:"allow_global_only"`
}

type DiscoverResponse struct {
	ProjectContext string                   `json:"project_context"`
	Mode           string                   `json:"mode"`
	Project        *project.ResolvedProject `json:"project,omitempty"`
	Permissions    PermissionsInfo          `json:"permissions"`
	GlobalIndex    ScopeIndex               `json:"global_index"`
	ProjectIndex   *ScopeIndex              `json:"project_index,omitempty"`
	LatestCursor   string                   `json:"latest_cursor"`
}

// 7.2 memory_query
type QueryRequest struct {
	ProjectContext string   `json:"project_context"`
	Scopes         []string `json:"scopes"`
	Query          string   `json:"query"`
	Categories     []string `json:"categories"`
	Tags           []string `json:"tags"`
	Status         []string `json:"status"`
	Cursor         *string  `json:"cursor"`
	Limit          int      `json:"limit"`
}

type QueryResponse struct {
	Items        []MemorySummary `json:"items"`
	NextCursor   *string         `json:"next_cursor,omitempty"`
	LatestCursor string          `json:"latest_cursor"`
}

// 7.3 memory_read
type ReadRequest struct {
	ProjectContext string `json:"project_context"`
	MemoryID       string `json:"memory_id"`
	Version        *int64 `json:"version"`
}

type ReadResponse struct {
	Memory       MemoryDetail `json:"memory"`
	LatestCursor string       `json:"latest_cursor"`
}

// 7.4 memory_create
type CreateRequest struct {
	ProjectContext string               `json:"project_context"`
	Scope          string               `json:"scope"`
	Category       string               `json:"category"`
	Title          string               `json:"title"`
	Content        string               `json:"content"`
	Tags           []string             `json:"tags"`
	IsPinned       bool                 `json:"is_pinned"`
	SourceRefs     []security.SourceRef `json:"source_refs"`
	IdempotencyKey string               `json:"idempotency_key"`
}

type WriteMemoryResponse struct {
	Memory              MemoryDetail `json:"memory"`
	DuplicateCandidates []uuid.UUID  `json:"duplicate_candidates,omitempty"`
	LatestCursor        string       `json:"latest_cursor"`
}

// 7.5 memory_update
type MemoryPatch struct {
	Category   *string               `json:"category,omitempty"`
	Title      *string               `json:"title,omitempty"`
	Content    *string               `json:"content,omitempty"`
	Tags       *[]string             `json:"tags,omitempty"`
	SourceRefs *[]security.SourceRef `json:"source_refs,omitempty"`
	Status     *string               `json:"status,omitempty"`
	IsPinned   *bool                 `json:"is_pinned,omitempty"`
}

type UpdateRequest struct {
	ProjectContext  string      `json:"project_context"`
	MemoryID        string      `json:"memory_id"`
	ExpectedVersion int64       `json:"expected_version"`
	Patch           MemoryPatch `json:"patch"`
	IdempotencyKey  string      `json:"idempotency_key"`
}

// 7.6 memory_delete
type DeleteRequest struct {
	ProjectContext  string `json:"project_context"`
	MemoryID        string `json:"memory_id"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type DeleteResponse struct {
	MemoryID     uuid.UUID `json:"memory_id"`
	Version      int64     `json:"version"`
	DeletedAt    time.Time `json:"deleted_at"`
	LatestCursor string    `json:"latest_cursor"`
}

// 7.7 memory_changes
type ChangesRequest struct {
	ProjectContext string `json:"project_context"`
	Cursor         string `json:"cursor"`
	Limit          int    `json:"limit"`
}

type ChangeEvent struct {
	Revision      int64      `json:"revision"`
	Scope         string     `json:"scope"`
	ProjectID     *uuid.UUID `json:"project_id,omitempty"`
	MemoryID      uuid.UUID  `json:"memory_id"`
	Operation     string     `json:"operation"`
	MemoryVersion int64      `json:"memory_version"`
	ActorID       uuid.UUID  `json:"actor_id"`
	CreatedAt     time.Time  `json:"created_at"`
}

type ChangesResponse struct {
	Events       []ChangeEvent `json:"events"`
	NextCursor   string        `json:"next_cursor"`
	HasMore      bool          `json:"has_more"`
	LatestCursor string        `json:"latest_cursor"`
}

// 7.8 memory_promote
type PromoteRequest struct {
	ProjectContext  string   `json:"project_context"`
	MemoryID        string   `json:"memory_id"`
	ExpectedVersion int64    `json:"expected_version"`
	Category        string   `json:"category,omitempty"`
	Title           string   `json:"title,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	IsPinned        *bool    `json:"is_pinned,omitempty"`
	IdempotencyKey  string   `json:"idempotency_key"`
}

// Management: Restore soft-deleted memory
type RestoreRequest struct {
	TenantID        uuid.UUID `json:"tenant_id"`
	ActorID         uuid.UUID `json:"actor_id"`
	MemoryID        uuid.UUID `json:"memory_id"`
	ExpectedVersion int64     `json:"expected_version"`
	RequestID       string    `json:"request_id"`
}
