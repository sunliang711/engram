package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"engram/internal/apperror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/scrypt"
)

const (
	SnapshotFormat  = "engram-system-snapshot"
	SnapshotVersion = 1
	maxImportBytes  = 64 << 20 // 64 MiB
)

// SnapshotManifest describes a full Engram system migration archive.
type SnapshotManifest struct {
	Format          string         `json:"format"`
	SnapshotVersion int            `json:"snapshot_version"`
	ExportedAt      time.Time      `json:"exported_at"`
	Encrypted       bool           `json:"encrypted"`
	KDF             string         `json:"kdf,omitempty"`
	KDFSalt         string         `json:"kdf_salt,omitempty"`
	PayloadSHA256   string         `json:"payload_sha256"`
	Counts          SnapshotCounts `json:"counts"`
}

// SnapshotCounts records row counts for each exported/imported entity type.
type SnapshotCounts struct {
	Tenants             int `json:"tenants"`
	Principals          int `json:"principals"`
	TenantMembers       int `json:"tenant_members"`
	Projects            int `json:"projects"`
	ProjectMembers      int `json:"project_members"`
	ProjectRepositories int `json:"project_repositories"`
	APITokens           int `json:"api_tokens"`
	Memories            int `json:"memories"`
	MemoryRevisions     int `json:"memory_revisions"`
	MemoryEvents        int `json:"memory_events"`
	TokensResealed      int `json:"tokens_resealed,omitempty"`
}

// SnapshotPayload contains all portable system data needed to restore an Engram instance.
type SnapshotPayload struct {
	Tenants             []snapshotTenant            `json:"tenants"`
	Principals          []snapshotPrincipal         `json:"principals"`
	TenantMembers       []snapshotTenantMember      `json:"tenant_members"`
	Projects            []snapshotProject           `json:"projects"`
	ProjectMembers      []snapshotProjectMember     `json:"project_members"`
	ProjectRepositories []snapshotProjectRepository `json:"project_repositories"`
	APITokens           []snapshotAPIToken          `json:"api_tokens"`
	Memories            []snapshotMemory            `json:"memories"`
	MemoryRevisions     []snapshotMemoryRevision    `json:"memory_revisions"`
	MemoryEvents        []snapshotMemoryEvent       `json:"memory_events"`
}

type snapshotTenant struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type snapshotPrincipal struct {
	ID               uuid.UUID `json:"id"`
	IdentityProvider string    `json:"identity_provider"`
	Subject          string    `json:"subject"`
	DisplayName      string    `json:"display_name"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

type snapshotTenantMember struct {
	TenantID    uuid.UUID `json:"tenant_id"`
	PrincipalID uuid.UUID `json:"principal_id"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

type snapshotProject struct {
	ID        uuid.UUID  `json:"id"`
	TenantID  uuid.UUID  `json:"tenant_id"`
	Name      string     `json:"name"`
	Marker    *uuid.UUID `json:"marker,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type snapshotProjectMember struct {
	ProjectID   uuid.UUID `json:"project_id"`
	PrincipalID uuid.UUID `json:"principal_id"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

type snapshotProjectRepository struct {
	ID              uuid.UUID `json:"id"`
	TenantID        uuid.UUID `json:"tenant_id"`
	ProjectID       uuid.UUID `json:"project_id"`
	CanonicalRemote string    `json:"canonical_remote"`
	WorkspacePath   string    `json:"workspace_path"`
	IsPrimary       bool      `json:"is_primary"`
	CreatedAt       time.Time `json:"created_at"`
}

type snapshotAPIToken struct {
	ID              uuid.UUID  `json:"id"`
	TokenHash       string     `json:"token_hash"`
	TokenPrefix     string     `json:"token_prefix"`
	TokenCiphertext string     `json:"token_ciphertext,omitempty"`
	RawToken        string     `json:"raw_token,omitempty"`
	TenantID        uuid.UUID  `json:"tenant_id"`
	PrincipalID     uuid.UUID  `json:"principal_id"`
	Name            string     `json:"name"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
}

type snapshotMemory struct {
	ID             uuid.UUID       `json:"id"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	Scope          string          `json:"scope"`
	ProjectID      *uuid.UUID      `json:"project_id,omitempty"`
	Category       string          `json:"category"`
	Title          string          `json:"title"`
	ContentMD      string          `json:"content_md"`
	Tags           []string        `json:"tags"`
	SourceRefs     json.RawMessage `json:"source_refs"`
	IsPinned       bool            `json:"is_pinned"`
	Status         string          `json:"status"`
	Version        int64           `json:"version"`
	SourceMemoryID *uuid.UUID      `json:"source_memory_id,omitempty"`
	ContentHash    string          `json:"content_hash"`
	CreatedBy      uuid.UUID       `json:"created_by"`
	UpdatedBy      uuid.UUID       `json:"updated_by"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	DeletedAt      *time.Time      `json:"deleted_at,omitempty"`
}

type snapshotMemoryRevision struct {
	ID        int64           `json:"id"`
	TenantID  uuid.UUID       `json:"tenant_id"`
	MemoryID  uuid.UUID       `json:"memory_id"`
	Version   int64           `json:"version"`
	Operation string          `json:"operation"`
	Snapshot  json.RawMessage `json:"snapshot"`
	ActorID   uuid.UUID       `json:"actor_id"`
	RequestID string          `json:"request_id"`
	CreatedAt time.Time       `json:"created_at"`
}

type snapshotMemoryEvent struct {
	Revision      int64      `json:"revision"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	ProjectID     *uuid.UUID `json:"project_id,omitempty"`
	MemoryID      uuid.UUID  `json:"memory_id"`
	Operation     string     `json:"operation"`
	MemoryVersion int64      `json:"memory_version"`
	ActorID       uuid.UUID  `json:"actor_id"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (h *Handler) handleMigrationExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "failed to read request body"))
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
	}

	payload, err := h.exportSnapshotPayload(r.Context(), req.Passphrase != "")
	if err != nil {
		writeJSONError(w, err)
		return
	}

	zipBytes, manifest, err := EncodeSnapshotArchive(payload, req.Passphrase, time.Now().UTC())
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "encode migration snapshot", err))
		return
	}

	h.log.Info().
		Int("tenants", manifest.Counts.Tenants).
		Int("projects", manifest.Counts.Projects).
		Int("memories", manifest.Counts.Memories).
		Bool("encrypted", manifest.Encrypted).
		Msg("exported full system migration snapshot")

	filename := fmt.Sprintf("engram-system-snapshot-%s.zip", manifest.ExportedAt.Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Snapshot-Encrypted", fmt.Sprintf("%t", manifest.Encrypted))
	w.Header().Set("X-Snapshot-Memories", fmt.Sprintf("%d", manifest.Counts.Memories))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(zipBytes)
}

func (h *Handler) handleMigrationImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(maxImportBytes); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid multipart upload or file exceeds 64MB limit"))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "snapshot file is required"))
		return
	}
	defer file.Close()

	zipBytes, err := io.ReadAll(io.LimitReader(file, maxImportBytes+1))
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "failed to read uploaded snapshot"))
		return
	}
	if len(zipBytes) > maxImportBytes {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "snapshot file exceeds 64MB limit"))
		return
	}

	passphrase := r.FormValue("passphrase")
	confirmReplace := strings.EqualFold(strings.TrimSpace(r.FormValue("confirm_replace")), "true")

	manifest, payload, err := DecodeSnapshotArchive(zipBytes, passphrase)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	counts, err := h.importSnapshotPayload(r.Context(), payload, confirmReplace)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	h.log.Info().
		Int("tenants", counts.Tenants).
		Int("projects", counts.Projects).
		Int("memories", counts.Memories).
		Int("tokens_resealed", counts.TokensResealed).
		Bool("encrypted", manifest.Encrypted).
		Msg("imported full system migration snapshot")

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "restored",
		"snapshot_version": manifest.SnapshotVersion,
		"exported_at":      manifest.ExportedAt,
		"encrypted":        manifest.Encrypted,
		"counts":           counts,
	})
}

func (h *Handler) exportSnapshotPayload(ctx context.Context, includeRawTokens bool) (SnapshotPayload, error) {
	var p SnapshotPayload
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "begin snapshot export tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. tenants
	rows, err := tx.Query(ctx, `SELECT id, name, status, created_at FROM tenants ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export tenants", err)
	}
	for rows.Next() {
		var row snapshotTenant
		if err := rows.Scan(&row.ID, &row.Name, &row.Status, &row.CreatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan tenant", err)
		}
		p.Tenants = append(p.Tenants, row)
	}
	rows.Close()

	// 2. principals
	rows, err = tx.Query(ctx, `SELECT id, identity_provider, subject, display_name, status, created_at FROM principals ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export principals", err)
	}
	for rows.Next() {
		var row snapshotPrincipal
		if err := rows.Scan(&row.ID, &row.IdentityProvider, &row.Subject, &row.DisplayName, &row.Status, &row.CreatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan principal", err)
		}
		p.Principals = append(p.Principals, row)
	}
	rows.Close()

	// 3. tenant_members
	rows, err = tx.Query(ctx, `SELECT tenant_id, principal_id, role, created_at FROM tenant_members ORDER BY created_at ASC, tenant_id ASC, principal_id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export tenant_members", err)
	}
	for rows.Next() {
		var row snapshotTenantMember
		if err := rows.Scan(&row.TenantID, &row.PrincipalID, &row.Role, &row.CreatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan tenant_member", err)
		}
		p.TenantMembers = append(p.TenantMembers, row)
	}
	rows.Close()

	// 4. projects
	rows, err = tx.Query(ctx, `SELECT id, tenant_id, name, marker, status, created_at, updated_at FROM projects ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export projects", err)
	}
	for rows.Next() {
		var row snapshotProject
		if err := rows.Scan(&row.ID, &row.TenantID, &row.Name, &row.Marker, &row.Status, &row.CreatedAt, &row.UpdatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan project", err)
		}
		p.Projects = append(p.Projects, row)
	}
	rows.Close()

	// 5. project_members
	rows, err = tx.Query(ctx, `SELECT project_id, principal_id, role, created_at FROM project_members ORDER BY created_at ASC, project_id ASC, principal_id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export project_members", err)
	}
	for rows.Next() {
		var row snapshotProjectMember
		if err := rows.Scan(&row.ProjectID, &row.PrincipalID, &row.Role, &row.CreatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan project_member", err)
		}
		p.ProjectMembers = append(p.ProjectMembers, row)
	}
	rows.Close()

	// 6. project_repositories
	rows, err = tx.Query(ctx, `SELECT id, tenant_id, project_id, canonical_remote, workspace_path, is_primary, created_at FROM project_repositories ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export project_repositories", err)
	}
	for rows.Next() {
		var row snapshotProjectRepository
		if err := rows.Scan(&row.ID, &row.TenantID, &row.ProjectID, &row.CanonicalRemote, &row.WorkspacePath, &row.IsPrimary, &row.CreatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan project_repository", err)
		}
		p.ProjectRepositories = append(p.ProjectRepositories, row)
	}
	rows.Close()

	// 7. api_tokens
	rows, err = tx.Query(ctx, `SELECT id, token_hash, token_prefix, token_ciphertext, tenant_id, principal_id, name, expires_at, revoked_at, created_at, last_used_at FROM api_tokens ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export api_tokens", err)
	}
	for rows.Next() {
		var row snapshotAPIToken
		if err := rows.Scan(&row.ID, &row.TokenHash, &row.TokenPrefix, &row.TokenCiphertext, &row.TenantID, &row.PrincipalID, &row.Name, &row.ExpiresAt, &row.RevokedAt, &row.CreatedAt, &row.LastUsedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan api_token", err)
		}
		if includeRawTokens && row.TokenCiphertext != "" {
			if raw, err := h.authMgr.UnsealToken(row.TokenCiphertext); err == nil {
				row.RawToken = raw
			}
		}
		p.APITokens = append(p.APITokens, row)
	}
	rows.Close()

	// 8. memories (including soft-deleted entries; fts_doc is generated by PostgreSQL)
	rows, err = tx.Query(ctx, `
		SELECT id, tenant_id, scope, project_id, category, title, content_md,
		       tags, source_refs, is_pinned, status, version, source_memory_id,
		       content_hash, created_by, updated_by, created_at, updated_at, deleted_at
		FROM memories
		ORDER BY created_at ASC, id ASC
	`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export memories", err)
	}
	for rows.Next() {
		var row snapshotMemory
		if err := rows.Scan(
			&row.ID, &row.TenantID, &row.Scope, &row.ProjectID, &row.Category, &row.Title, &row.ContentMD,
			&row.Tags, &row.SourceRefs, &row.IsPinned, &row.Status, &row.Version, &row.SourceMemoryID,
			&row.ContentHash, &row.CreatedBy, &row.UpdatedBy, &row.CreatedAt, &row.UpdatedAt, &row.DeletedAt,
		); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan memory", err)
		}
		if row.Tags == nil {
			row.Tags = []string{}
		}
		if len(row.SourceRefs) == 0 {
			row.SourceRefs = json.RawMessage("[]")
		}
		p.Memories = append(p.Memories, row)
	}
	rows.Close()

	// 9. memory_revisions
	rows, err = tx.Query(ctx, `SELECT id, tenant_id, memory_id, version, operation, snapshot, actor_id, request_id, created_at FROM memory_revisions ORDER BY id ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export memory_revisions", err)
	}
	for rows.Next() {
		var row snapshotMemoryRevision
		if err := rows.Scan(&row.ID, &row.TenantID, &row.MemoryID, &row.Version, &row.Operation, &row.Snapshot, &row.ActorID, &row.RequestID, &row.CreatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan memory_revision", err)
		}
		p.MemoryRevisions = append(p.MemoryRevisions, row)
	}
	rows.Close()

	// 10. memory_events
	rows, err = tx.Query(ctx, `SELECT revision, tenant_id, project_id, memory_id, operation, memory_version, actor_id, created_at FROM memory_events ORDER BY revision ASC`)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export memory_events", err)
	}
	for rows.Next() {
		var row snapshotMemoryEvent
		if err := rows.Scan(&row.Revision, &row.TenantID, &row.ProjectID, &row.MemoryID, &row.Operation, &row.MemoryVersion, &row.ActorID, &row.CreatedAt); err != nil {
			rows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan memory_event", err)
		}
		p.MemoryEvents = append(p.MemoryEvents, row)
	}
	rows.Close()

	if err := tx.Commit(ctx); err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "commit snapshot export tx", err)
	}
	return p, nil
}

func (h *Handler) importSnapshotPayload(ctx context.Context, p SnapshotPayload, confirmReplace bool) (SnapshotCounts, error) {
	var counts SnapshotCounts
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return counts, apperror.Wrap(apperror.CodeInternalError, "begin snapshot import tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingRows int
	err = tx.QueryRow(ctx, `
		SELECT (SELECT count(*)::int FROM tenants)
		     + (SELECT count(*)::int FROM principals)
		     + (SELECT count(*)::int FROM projects)
		     + (SELECT count(*)::int FROM memories)
		     + (SELECT count(*)::int FROM api_tokens)
	`).Scan(&existingRows)
	if err != nil {
		return counts, apperror.Wrap(apperror.CodeInternalError, "check target database state", err)
	}
	if existingRows > 0 && !confirmReplace {
		return counts, apperror.New(apperror.CodeVersionConflict, "目标服务已存在业务数据，请勾选“确认清空现有业务数据并覆盖还原”后再导入")
	}

	// Preserve admin_accounts and schema_migrations; replace all tenant/project/memory state atomically.
	if _, err := tx.Exec(ctx, `
		TRUNCATE TABLE
			idempotency_requests,
			memory_events,
			memory_revisions,
			memories,
			project_repositories,
			project_members,
			projects,
			api_tokens,
			tenant_members,
			principals,
			tenants
		RESTART IDENTITY CASCADE
	`); err != nil {
		return counts, apperror.Wrap(apperror.CodeInternalError, "clear existing business tables", err)
	}

	for _, t := range p.Tenants {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tenants (id, name, status, created_at)
			VALUES ($1, $2, $3, $4)
		`, t.ID, t.Name, t.Status, t.CreatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore tenant "+t.ID.String(), err)
		}
	}
	counts.Tenants = len(p.Tenants)

	for _, pr := range p.Principals {
		if _, err := tx.Exec(ctx, `
			INSERT INTO principals (id, identity_provider, subject, display_name, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, pr.ID, pr.IdentityProvider, pr.Subject, pr.DisplayName, pr.Status, pr.CreatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore principal "+pr.ID.String(), err)
		}
	}
	counts.Principals = len(p.Principals)

	for _, tm := range p.TenantMembers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tenant_members (tenant_id, principal_id, role, created_at)
			VALUES ($1, $2, $3, $4)
		`, tm.TenantID, tm.PrincipalID, tm.Role, tm.CreatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore tenant_member", err)
		}
	}
	counts.TenantMembers = len(p.TenantMembers)

	for _, proj := range p.Projects {
		if _, err := tx.Exec(ctx, `
			INSERT INTO projects (id, tenant_id, name, marker, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, proj.ID, proj.TenantID, proj.Name, proj.Marker, proj.Status, proj.CreatedAt, proj.UpdatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore project "+proj.ID.String(), err)
		}
	}
	counts.Projects = len(p.Projects)

	for _, pm := range p.ProjectMembers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO project_members (project_id, principal_id, role, created_at)
			VALUES ($1, $2, $3, $4)
		`, pm.ProjectID, pm.PrincipalID, pm.Role, pm.CreatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore project_member", err)
		}
	}
	counts.ProjectMembers = len(p.ProjectMembers)

	for _, repo := range p.ProjectRepositories {
		if _, err := tx.Exec(ctx, `
			INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, repo.ID, repo.TenantID, repo.ProjectID, repo.CanonicalRemote, repo.WorkspacePath, repo.IsPrimary, repo.CreatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore project_repository "+repo.ID.String(), err)
		}
	}
	counts.ProjectRepositories = len(p.ProjectRepositories)

	for _, tk := range p.APITokens {
		ciphertext := tk.TokenCiphertext
		if tk.RawToken != "" {
			sealed, err := h.authMgr.SealToken(tk.RawToken)
			if err != nil {
				return counts, apperror.Wrap(apperror.CodeInternalError, "reseal api token", err)
			}
			ciphertext = sealed
			counts.TokensResealed++
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO api_tokens (id, token_hash, token_prefix, token_ciphertext, tenant_id, principal_id, name, expires_at, revoked_at, created_at, last_used_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, tk.ID, tk.TokenHash, tk.TokenPrefix, ciphertext, tk.TenantID, tk.PrincipalID, tk.Name, tk.ExpiresAt, tk.RevokedAt, tk.CreatedAt, tk.LastUsedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore api_token "+tk.ID.String(), err)
		}
	}
	counts.APITokens = len(p.APITokens)

	// Insert memories with source_memory_id = NULL first to avoid self-referential ordering issues, then backfill links.
	for _, m := range p.Memories {
		tags := m.Tags
		if tags == nil {
			tags = []string{}
		}
		refs := m.SourceRefs
		if len(refs) == 0 {
			refs = json.RawMessage("[]")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO memories (
				id, tenant_id, scope, project_id, category, title, content_md,
				tags, source_refs, is_pinned, status, version, source_memory_id,
				content_hash, created_by, updated_by, created_at, updated_at, deleted_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, NULL,
				$13, $14, $15, $16, $17, $18
			)
		`, m.ID, m.TenantID, m.Scope, m.ProjectID, m.Category, m.Title, m.ContentMD,
			tags, refs, m.IsPinned, m.Status, m.Version,
			m.ContentHash, m.CreatedBy, m.UpdatedBy, m.CreatedAt, m.UpdatedAt, m.DeletedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore memory "+m.ID.String(), err)
		}
	}
	for _, m := range p.Memories {
		if m.SourceMemoryID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE memories SET source_memory_id = $2 WHERE id = $1
			`, m.ID, *m.SourceMemoryID); err != nil {
				return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore memory source_memory_id "+m.ID.String(), err)
			}
		}
	}
	counts.Memories = len(p.Memories)

	for _, rev := range p.MemoryRevisions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO memory_revisions (id, tenant_id, memory_id, version, operation, snapshot, actor_id, request_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, rev.ID, rev.TenantID, rev.MemoryID, rev.Version, rev.Operation, rev.Snapshot, rev.ActorID, rev.RequestID, rev.CreatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore memory_revision", err)
		}
	}
	counts.MemoryRevisions = len(p.MemoryRevisions)

	for _, ev := range p.MemoryEvents {
		if _, err := tx.Exec(ctx, `
			INSERT INTO memory_events (revision, tenant_id, project_id, memory_id, operation, memory_version, actor_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, ev.Revision, ev.TenantID, ev.ProjectID, ev.MemoryID, ev.Operation, ev.MemoryVersion, ev.ActorID, ev.CreatedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "restore memory_event", err)
		}
	}
	counts.MemoryEvents = len(p.MemoryEvents)

	// Re-sync BIGSERIAL sequences after explicit id/revision inserts.
	if _, err := tx.Exec(ctx, `
		SELECT setval(
			pg_get_serial_sequence('memory_revisions', 'id'),
			COALESCE((SELECT MAX(id) FROM memory_revisions), 1),
			EXISTS (SELECT 1 FROM memory_revisions)
		);
		SELECT setval(
			pg_get_serial_sequence('memory_events', 'revision'),
			COALESCE((SELECT MAX(revision) FROM memory_events), 1),
			EXISTS (SELECT 1 FROM memory_events)
		);
	`); err != nil {
		return counts, apperror.Wrap(apperror.CodeInternalError, "reset serial sequences", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return counts, apperror.Wrap(apperror.CodeInternalError, "commit snapshot import tx", err)
	}
	return counts, nil
}

// EncodeSnapshotArchive packages SnapshotPayload and its manifest into a ZIP archive.
// When passphrase is non-empty, payload JSON is encrypted with scrypt + AES-256-GCM.
func EncodeSnapshotArchive(payload SnapshotPayload, passphrase string, exportedAt time.Time) ([]byte, SnapshotManifest, error) {
	plainJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, SnapshotManifest{}, err
	}
	sum := sha256.Sum256(plainJSON)
	manifest := SnapshotManifest{
		Format:          SnapshotFormat,
		SnapshotVersion: SnapshotVersion,
		ExportedAt:      exportedAt.UTC(),
		Encrypted:       passphrase != "",
		PayloadSHA256:   hex.EncodeToString(sum[:]),
		Counts: SnapshotCounts{
			Tenants:             len(payload.Tenants),
			Principals:          len(payload.Principals),
			TenantMembers:       len(payload.TenantMembers),
			Projects:            len(payload.Projects),
			ProjectMembers:      len(payload.ProjectMembers),
			ProjectRepositories: len(payload.ProjectRepositories),
			APITokens:           len(payload.APITokens),
			Memories:            len(payload.Memories),
			MemoryRevisions:     len(payload.MemoryRevisions),
			MemoryEvents:        len(payload.MemoryEvents),
		},
	}

	payloadEntryName := "payload.json"
	payloadEntryBytes := plainJSON
	if passphrase != "" {
		cipherBytes, saltB64, err := sealSnapshotPayload(plainJSON, passphrase)
		if err != nil {
			return nil, SnapshotManifest{}, err
		}
		manifest.KDF = "scrypt-aes-256-gcm"
		manifest.KDFSalt = saltB64
		payloadEntryName = "payload.enc"
		payloadEntryBytes = cipherBytes
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, SnapshotManifest{}, err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{name: "manifest.json", data: manifestBytes},
		{name: payloadEntryName, data: payloadEntryBytes},
	} {
		hdr := &zip.FileHeader{
			Name:     entry.name,
			Method:   zip.Deflate,
			Modified: exportedAt.UTC(),
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()
			return nil, SnapshotManifest{}, err
		}
		if _, err := fw.Write(entry.data); err != nil {
			_ = zw.Close()
			return nil, SnapshotManifest{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, SnapshotManifest{}, err
	}
	return buf.Bytes(), manifest, nil
}

// DecodeSnapshotArchive validates and extracts a migration ZIP archive, decrypting if encrypted.
func DecodeSnapshotArchive(zipBytes []byte, passphrase string) (SnapshotManifest, SnapshotPayload, error) {
	var manifest SnapshotManifest
	var payload SnapshotPayload

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "invalid zip snapshot archive")
	}

	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	mf, ok := files["manifest.json"]
	if !ok {
		return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "snapshot archive missing manifest.json")
	}
	manifestBytes, err := readZipEntryLimited(mf, 1<<20)
	if err != nil {
		return manifest, payload, err
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "invalid manifest.json")
	}
	if manifest.Format != SnapshotFormat {
		return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "unsupported snapshot format")
	}
	if manifest.SnapshotVersion != SnapshotVersion {
		return manifest, payload, apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("unsupported snapshot_version: %d", manifest.SnapshotVersion))
	}

	var plainJSON []byte
	if manifest.Encrypted {
		if passphrase == "" {
			return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "该迁移包已加密，请输入导出时设置的加密口令")
		}
		ef, ok := files["payload.enc"]
		if !ok {
			return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "encrypted snapshot archive missing payload.enc")
		}
		cipherBytes, err := readZipEntryLimited(ef, maxImportBytes)
		if err != nil {
			return manifest, payload, err
		}
		plainJSON, err = unsealSnapshotPayload(cipherBytes, passphrase, manifest.KDFSalt)
		if err != nil {
			return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "解密失败：口令错误或迁移包已损坏")
		}
	} else {
		pf, ok := files["payload.json"]
		if !ok {
			return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "snapshot archive missing payload.json")
		}
		plainJSON, err = readZipEntryLimited(pf, maxImportBytes)
		if err != nil {
			return manifest, payload, err
		}
	}

	sum := sha256.Sum256(plainJSON)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), manifest.PayloadSHA256) {
		return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "快照校验失败：payload SHA-256 不匹配")
	}
	if err := json.Unmarshal(plainJSON, &payload); err != nil {
		return manifest, payload, apperror.New(apperror.CodeInvalidArgument, "invalid snapshot payload json")
	}
	return manifest, payload, nil
}

func readZipEntryLimited(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "failed to open zip entry "+f.Name)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "failed to read zip entry "+f.Name)
	}
	if int64(len(data)) > limit {
		return nil, apperror.New(apperror.CodeInvalidArgument, "zip entry exceeds size limit: "+f.Name)
	}
	return data, nil
}

func sealSnapshotPayload(plainJSON []byte, passphrase string) ([]byte, string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, "", err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, "", err
	}
	sealed := gcm.Seal(nonce, nonce, plainJSON, nil)
	return sealed, base64.StdEncoding.EncodeToString(salt), nil
}

func unsealSnapshotPayload(cipherBytes []byte, passphrase, saltB64 string) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil || len(salt) == 0 {
		return nil, errors.New("invalid kdf salt")
	}
	key, err := scrypt.Key([]byte(passphrase), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(cipherBytes) < nonceSize {
		return nil, errors.New("truncated ciphertext")
	}
	return gcm.Open(nil, cipherBytes[:nonceSize], cipherBytes[nonceSize:], nil)
}
