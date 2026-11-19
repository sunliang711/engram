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
	"sort"
	"strings"
	"time"

	"engram/internal/apperror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/scrypt"
)

const (
	SnapshotFormat         = "engram-system-snapshot"
	KnowledgeBundleFormat  = "engram-knowledge-bundle"
	SnapshotVersion        = 1
	KnowledgeBundleVersion = 1
	MigrationModeSystem    = "system"
	MigrationModeKnowledge = "knowledge"
	maxImportBytes         = 64 << 20 // 64 MiB
)

// SnapshotManifest describes an Engram migration archive (full system or knowledge-only).
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

// SnapshotCounts records row counts for exported and imported entities.
type SnapshotCounts struct {
	Tenants             int `json:"tenants"`
	Principals          int `json:"principals,omitempty"`
	TenantMembers       int `json:"tenant_members,omitempty"`
	Projects            int `json:"projects"`
	ProjectMembers      int `json:"project_members,omitempty"`
	ProjectRepositories int `json:"project_repositories"`
	APITokens           int `json:"api_tokens,omitempty"`
	Memories            int `json:"memories"`
	MemoryRevisions     int `json:"memory_revisions"`
	MemoryEvents        int `json:"memory_events,omitempty"`
	TokensResealed      int `json:"tokens_resealed,omitempty"`
	TenantsCreated      int `json:"tenants_created,omitempty"`
	TenantsMatched      int `json:"tenants_matched,omitempty"`
	ProjectsCreated     int `json:"projects_created,omitempty"`
	ProjectsMatched     int `json:"projects_matched,omitempty"`
	MemoriesImported    int `json:"memories_imported,omitempty"`
	MemoriesSkipped     int `json:"memories_skipped,omitempty"`
	RevisionsImported   int `json:"revisions_imported,omitempty"`
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

// KnowledgeBundlePayload contains portable knowledge data (tenants, projects, remotes, memories, revisions)
// without principals, memberships, or API tokens.
type KnowledgeBundlePayload struct {
	Tenants             []snapshotTenant            `json:"tenants"`
	Projects            []snapshotProject           `json:"projects"`
	ProjectRepositories []snapshotProjectRepository `json:"project_repositories"`
	Memories            []knowledgeMemory           `json:"memories"`
	MemoryRevisions     []knowledgeMemoryRevision   `json:"memory_revisions"`
}

type knowledgeMemory struct {
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
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	DeletedAt      *time.Time      `json:"deleted_at,omitempty"`
}

type knowledgeMemoryRevision struct {
	TenantID  uuid.UUID       `json:"tenant_id"`
	MemoryID  uuid.UUID       `json:"memory_id"`
	Version   int64           `json:"version"`
	Operation string          `json:"operation"`
	Snapshot  json.RawMessage `json:"snapshot"`
	RequestID string          `json:"request_id"`
	CreatedAt time.Time       `json:"created_at"`
}

// KnowledgeExportOptions configures which tenants and deletion states to export in a knowledge bundle.
type KnowledgeExportOptions struct {
	TenantIDs      []uuid.UUID
	IncludeDeleted bool
}

// KnowledgeImportOptions configures how a knowledge bundle maps source tenants onto target tenants.
type KnowledgeImportOptions struct {
	TenantMode     string            // "by_name" (default) or "single"
	TargetTenantID uuid.UUID         // used when TenantMode == "single"
	TenantMap      map[string]string // optional source_tenant_id -> target_tenant_id
}

// InspectTenantSummary summarizes one tenant contained inside a migration archive.
type InspectTenantSummary struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	ProjectsCount int       `json:"projects_count"`
	MemoriesCount int       `json:"memories_count"`
}

// DecodedArchive holds the parsed manifest and typed payload of either archive format.
type DecodedArchive struct {
	Manifest        SnapshotManifest
	SystemPayload   *SnapshotPayload
	KnowledgeBundle *KnowledgeBundlePayload
}

func (h *Handler) handleMigrationExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Mode           string   `json:"mode"`
		Passphrase     string   `json:"passphrase"`
		TenantIDs      []string `json:"tenant_ids"`
		IncludeDeleted *bool    `json:"include_deleted"`
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

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = MigrationModeSystem
	}

	var zipBytes []byte
	var manifest SnapshotManifest
	var filename string

	switch mode {
	case MigrationModeSystem:
		payload, err := h.exportSnapshotPayload(r.Context(), req.Passphrase != "")
		if err != nil {
			writeJSONError(w, err)
			return
		}
		zipBytes, manifest, err = EncodeSnapshotArchive(payload, req.Passphrase, time.Now().UTC())
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "encode migration snapshot", err))
			return
		}
		filename = fmt.Sprintf("engram-system-snapshot-%s.zip", manifest.ExportedAt.Format("20060102T150405Z"))
		h.log.Info().
			Int("tenants", manifest.Counts.Tenants).
			Int("projects", manifest.Counts.Projects).
			Int("memories", manifest.Counts.Memories).
			Bool("encrypted", manifest.Encrypted).
			Msg("exported full system migration snapshot")

	case MigrationModeKnowledge:
		tenantIDs := make([]uuid.UUID, 0, len(req.TenantIDs))
		seen := make(map[uuid.UUID]bool, len(req.TenantIDs))
		for _, raw := range req.TenantIDs {
			id, parseErr := uuid.Parse(strings.TrimSpace(raw))
			if parseErr != nil {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id: "+raw))
				return
			}
			if !seen[id] {
				seen[id] = true
				tenantIDs = append(tenantIDs, id)
			}
		}
		includeDeleted := true
		if req.IncludeDeleted != nil {
			includeDeleted = *req.IncludeDeleted
		}
		bundle, err := h.exportKnowledgeBundlePayload(r.Context(), KnowledgeExportOptions{
			TenantIDs:      tenantIDs,
			IncludeDeleted: includeDeleted,
		})
		if err != nil {
			writeJSONError(w, err)
			return
		}
		zipBytes, manifest, err = EncodeKnowledgeArchive(bundle, req.Passphrase, time.Now().UTC())
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "encode knowledge bundle", err))
			return
		}
		filename = fmt.Sprintf("engram-knowledge-bundle-%s.zip", manifest.ExportedAt.Format("20060102T150405Z"))
		h.log.Info().
			Int("tenants", manifest.Counts.Tenants).
			Int("projects", manifest.Counts.Projects).
			Int("memories", manifest.Counts.Memories).
			Bool("include_deleted", includeDeleted).
			Bool("encrypted", manifest.Encrypted).
			Msg("exported knowledge base migration bundle")

	default:
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid migration export mode"))
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Snapshot-Format", manifest.Format)
	w.Header().Set("X-Snapshot-Encrypted", fmt.Sprintf("%t", manifest.Encrypted))
	w.Header().Set("X-Snapshot-Tenants", fmt.Sprintf("%d", manifest.Counts.Tenants))
	w.Header().Set("X-Snapshot-Memories", fmt.Sprintf("%d", manifest.Counts.Memories))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(zipBytes)
}

func readUploadedZip(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(maxImportBytes); err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "invalid multipart upload or file exceeds 64MB limit")
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "snapshot file is required")
	}
	defer file.Close()

	zipBytes, err := io.ReadAll(io.LimitReader(file, maxImportBytes+1))
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "failed to read uploaded snapshot")
	}
	if len(zipBytes) > maxImportBytes {
		return nil, apperror.New(apperror.CodeInvalidArgument, "snapshot file exceeds 64MB limit")
	}
	return zipBytes, nil
}

func (h *Handler) handleMigrationInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	zipBytes, err := readUploadedZip(r)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	passphrase := r.FormValue("passphrase")

	manifest, err := InspectArchiveManifest(zipBytes)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	targetHasData, err := h.targetHasBusinessData(r.Context())
	if err != nil {
		writeJSONError(w, err)
		return
	}

	if manifest.Encrypted && passphrase == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"format":           manifest.Format,
			"snapshot_version": manifest.SnapshotVersion,
			"exported_at":      manifest.ExportedAt,
			"encrypted":        true,
			"needs_passphrase": true,
			"counts":           manifest.Counts,
			"tenants":          []InspectTenantSummary{},
			"target_has_data":  targetHasData,
		})
		return
	}

	decoded, err := DecodeMigrationArchive(zipBytes, passphrase)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	tenants := summarizeArchiveTenants(decoded)
	writeJSON(w, http.StatusOK, map[string]any{
		"format":           decoded.Manifest.Format,
		"snapshot_version": decoded.Manifest.SnapshotVersion,
		"exported_at":      decoded.Manifest.ExportedAt,
		"encrypted":        decoded.Manifest.Encrypted,
		"needs_passphrase": false,
		"counts":           decoded.Manifest.Counts,
		"tenants":          tenants,
		"target_has_data":  targetHasData,
	})
}

func (h *Handler) handleMigrationImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	zipBytes, err := readUploadedZip(r)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	passphrase := r.FormValue("passphrase")
	confirmReplace := strings.EqualFold(strings.TrimSpace(r.FormValue("confirm_replace")), "true")

	decoded, err := DecodeMigrationArchive(zipBytes, passphrase)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	switch decoded.Manifest.Format {
	case SnapshotFormat:
		counts, err := h.importSnapshotPayload(r.Context(), *decoded.SystemPayload, confirmReplace)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		h.log.Info().
			Int("tenants", counts.Tenants).
			Int("projects", counts.Projects).
			Int("memories", counts.Memories).
			Int("tokens_resealed", counts.TokensResealed).
			Bool("encrypted", decoded.Manifest.Encrypted).
			Msg("imported full system migration snapshot")

		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "restored",
			"format":           decoded.Manifest.Format,
			"snapshot_version": decoded.Manifest.SnapshotVersion,
			"exported_at":      decoded.Manifest.ExportedAt,
			"encrypted":        decoded.Manifest.Encrypted,
			"counts":           counts,
		})

	case KnowledgeBundleFormat:
		opts := KnowledgeImportOptions{
			TenantMode: strings.TrimSpace(r.FormValue("tenant_mode")),
		}
		if rawTarget := strings.TrimSpace(r.FormValue("target_tenant_id")); rawTarget != "" {
			tid, parseErr := uuid.Parse(rawTarget)
			if parseErr != nil {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid target_tenant_id"))
				return
			}
			opts.TargetTenantID = tid
		}
		if rawMap := strings.TrimSpace(r.FormValue("tenant_map")); rawMap != "" {
			if err := json.Unmarshal([]byte(rawMap), &opts.TenantMap); err != nil {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_map json"))
				return
			}
		}

		counts, err := h.importKnowledgeBundlePayload(r.Context(), *decoded.KnowledgeBundle, opts)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		h.log.Info().
			Int("tenants_created", counts.TenantsCreated).
			Int("tenants_matched", counts.TenantsMatched).
			Int("projects_created", counts.ProjectsCreated).
			Int("projects_matched", counts.ProjectsMatched).
			Int("memories_imported", counts.MemoriesImported).
			Int("memories_skipped", counts.MemoriesSkipped).
			Int("revisions_imported", counts.RevisionsImported).
			Bool("encrypted", decoded.Manifest.Encrypted).
			Msg("imported knowledge base migration bundle")

		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "imported",
			"format":           decoded.Manifest.Format,
			"snapshot_version": decoded.Manifest.SnapshotVersion,
			"exported_at":      decoded.Manifest.ExportedAt,
			"encrypted":        decoded.Manifest.Encrypted,
			"counts":           counts,
		})

	default:
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "unsupported snapshot format"))
	}
}

func (h *Handler) targetHasBusinessData(ctx context.Context) (bool, error) {
	var existingRows int
	err := h.pool.QueryRow(ctx, `
		SELECT (SELECT count(*)::int FROM tenants)
		     + (SELECT count(*)::int FROM principals)
		     + (SELECT count(*)::int FROM projects)
		     + (SELECT count(*)::int FROM memories)
		     + (SELECT count(*)::int FROM api_tokens)
	`).Scan(&existingRows)
	if err != nil {
		return false, apperror.Wrap(apperror.CodeInternalError, "check target database state", err)
	}
	return existingRows > 0, nil
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

// importSnapshotPayload restores data while applying current member permission boundaries.
func (h *Handler) importSnapshotPayload(ctx context.Context, p SnapshotPayload, confirmReplace bool) (SnapshotCounts, error) {
	p = normalizeSnapshotPermissions(p)
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

func (h *Handler) exportKnowledgeBundlePayload(ctx context.Context, opts KnowledgeExportOptions) (KnowledgeBundlePayload, error) {
	var p KnowledgeBundlePayload
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "begin knowledge export tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. tenants
	var tenantRows pgx.Rows
	if len(opts.TenantIDs) > 0 {
		tenantRows, err = tx.Query(ctx, `
			SELECT id, name, status, created_at
			FROM tenants
			WHERE id = ANY($1)
			ORDER BY created_at ASC, id ASC
		`, opts.TenantIDs)
	} else {
		tenantRows, err = tx.Query(ctx, `
			SELECT id, name, status, created_at
			FROM tenants
			ORDER BY created_at ASC, id ASC
		`)
	}
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export bundle tenants", err)
	}
	tenantIDs := make([]uuid.UUID, 0)
	for tenantRows.Next() {
		var row snapshotTenant
		if err := tenantRows.Scan(&row.ID, &row.Name, &row.Status, &row.CreatedAt); err != nil {
			tenantRows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan bundle tenant", err)
		}
		p.Tenants = append(p.Tenants, row)
		tenantIDs = append(tenantIDs, row.ID)
	}
	tenantRows.Close()

	if len(opts.TenantIDs) > 0 && len(p.Tenants) != len(opts.TenantIDs) {
		return p, apperror.New(apperror.CodeInvalidArgument, "部分选中的租户不存在，请刷新后重试")
	}
	if len(tenantIDs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return p, apperror.Wrap(apperror.CodeInternalError, "commit empty knowledge export tx", err)
		}
		return p, nil
	}

	// 2. projects
	projRows, err := tx.Query(ctx, `
		SELECT id, tenant_id, name, marker, status, created_at, updated_at
		FROM projects
		WHERE tenant_id = ANY($1)
		ORDER BY created_at ASC, id ASC
	`, tenantIDs)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export bundle projects", err)
	}
	for projRows.Next() {
		var row snapshotProject
		if err := projRows.Scan(&row.ID, &row.TenantID, &row.Name, &row.Marker, &row.Status, &row.CreatedAt, &row.UpdatedAt); err != nil {
			projRows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan bundle project", err)
		}
		p.Projects = append(p.Projects, row)
	}
	projRows.Close()

	// 3. project_repositories
	repoRows, err := tx.Query(ctx, `
		SELECT id, tenant_id, project_id, canonical_remote, workspace_path, is_primary, created_at
		FROM project_repositories
		WHERE tenant_id = ANY($1)
		ORDER BY created_at ASC, id ASC
	`, tenantIDs)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export bundle project_repositories", err)
	}
	for repoRows.Next() {
		var row snapshotProjectRepository
		if err := repoRows.Scan(&row.ID, &row.TenantID, &row.ProjectID, &row.CanonicalRemote, &row.WorkspacePath, &row.IsPrimary, &row.CreatedAt); err != nil {
			repoRows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan bundle project_repository", err)
		}
		p.ProjectRepositories = append(p.ProjectRepositories, row)
	}
	repoRows.Close()

	// 4. memories (without principal UUIDs)
	memQuery := `
		SELECT id, tenant_id, scope, project_id, category, title, content_md,
		       tags, source_refs, is_pinned, status, version, source_memory_id,
		       content_hash, created_at, updated_at, deleted_at
		FROM memories
		WHERE tenant_id = ANY($1)
	`
	if !opts.IncludeDeleted {
		memQuery += ` AND deleted_at IS NULL`
	}
	memQuery += ` ORDER BY created_at ASC, id ASC`

	memRows, err := tx.Query(ctx, memQuery, tenantIDs)
	if err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "export bundle memories", err)
	}
	memoryIDs := make([]uuid.UUID, 0)
	for memRows.Next() {
		var row knowledgeMemory
		if err := memRows.Scan(
			&row.ID, &row.TenantID, &row.Scope, &row.ProjectID, &row.Category, &row.Title, &row.ContentMD,
			&row.Tags, &row.SourceRefs, &row.IsPinned, &row.Status, &row.Version, &row.SourceMemoryID,
			&row.ContentHash, &row.CreatedAt, &row.UpdatedAt, &row.DeletedAt,
		); err != nil {
			memRows.Close()
			return p, apperror.Wrap(apperror.CodeInternalError, "scan bundle memory", err)
		}
		if row.Tags == nil {
			row.Tags = []string{}
		}
		if len(row.SourceRefs) == 0 {
			row.SourceRefs = json.RawMessage("[]")
		}
		p.Memories = append(p.Memories, row)
		memoryIDs = append(memoryIDs, row.ID)
	}
	memRows.Close()

	// 5. memory_revisions (without actor_id)
	if len(memoryIDs) > 0 {
		revRows, err := tx.Query(ctx, `
			SELECT tenant_id, memory_id, version, operation, snapshot, request_id, created_at
			FROM memory_revisions
			WHERE memory_id = ANY($1)
			ORDER BY memory_id ASC, version ASC, id ASC
		`, memoryIDs)
		if err != nil {
			return p, apperror.Wrap(apperror.CodeInternalError, "export bundle memory_revisions", err)
		}
		for revRows.Next() {
			var row knowledgeMemoryRevision
			if err := revRows.Scan(&row.TenantID, &row.MemoryID, &row.Version, &row.Operation, &row.Snapshot, &row.RequestID, &row.CreatedAt); err != nil {
				revRows.Close()
				return p, apperror.Wrap(apperror.CodeInternalError, "scan bundle memory_revision", err)
			}
			p.MemoryRevisions = append(p.MemoryRevisions, row)
		}
		revRows.Close()
	}

	if err := tx.Commit(ctx); err != nil {
		return p, apperror.Wrap(apperror.CodeInternalError, "commit knowledge export tx", err)
	}
	return p, nil
}

// importKnowledgeBundlePayload imports knowledge and audit history without activating target tenants.
func (h *Handler) importKnowledgeBundlePayload(ctx context.Context, p KnowledgeBundlePayload, opts KnowledgeImportOptions) (SnapshotCounts, error) {
	var counts SnapshotCounts
	mode := strings.TrimSpace(opts.TenantMode)
	if mode == "" {
		mode = "by_name"
	}
	if mode != "by_name" && mode != "single" {
		return counts, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_mode")
	}
	if err := validateKnowledgeBundle(p); err != nil {
		return counts, err
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return counts, apperror.Wrap(apperror.CodeInternalError, "begin knowledge import tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantIDMap, matchedTenants, createdTenants, err := resolveKnowledgeTenants(ctx, tx, p.Tenants, mode, opts)
	if err != nil {
		return counts, err
	}
	counts.TenantsMatched = len(matchedTenants)
	counts.TenantsCreated = len(createdTenants)
	counts.Tenants = counts.TenantsMatched + counts.TenantsCreated

	actorByTenant := make(map[uuid.UUID]uuid.UUID)
	for _, targetTID := range tenantIDMap {
		if _, ok := actorByTenant[targetTID]; ok {
			continue
		}
		// Tenant resolution already validated the target; imports must preserve disabled status.
		actorID, err := ensureConsoleAuditActorTx(ctx, tx)
		if err != nil {
			return counts, err
		}
		actorByTenant[targetTID] = actorID
	}

	reposByProj := make(map[uuid.UUID][]snapshotProjectRepository)
	for _, repo := range p.ProjectRepositories {
		reposByProj[repo.ProjectID] = append(reposByProj[repo.ProjectID], repo)
	}

	projectIDMap, projMatched, projCreated, repoInserted, err := resolveKnowledgeProjects(ctx, tx, p.Projects, reposByProj, tenantIDMap)
	if err != nil {
		return counts, err
	}
	counts.ProjectsMatched = len(projMatched)
	counts.ProjectsCreated = len(projCreated)
	counts.Projects = counts.ProjectsMatched + counts.ProjectsCreated
	counts.ProjectRepositories = repoInserted

	revsByMem := make(map[uuid.UUID][]knowledgeMemoryRevision)
	for _, rev := range p.MemoryRevisions {
		revsByMem[rev.MemoryID] = append(revsByMem[rev.MemoryID], rev)
	}
	for memID := range revsByMem {
		revs := revsByMem[memID]
		sort.Slice(revs, func(i, j int) bool { return revs[i].Version < revs[j].Version })
		revsByMem[memID] = revs
	}

	memoryIDMap := make(map[uuid.UUID]uuid.UUID, len(p.Memories))
	importedSet := make(map[uuid.UUID]bool, len(p.Memories))

	for _, m := range p.Memories {
		targetTID := tenantIDMap[m.TenantID]
		actorID := actorByTenant[targetTID]
		var targetPID *uuid.UUID
		if m.Scope == "project" && m.ProjectID != nil {
			pid := projectIDMap[*m.ProjectID]
			targetPID = &pid
		}

		// Deduplicate within the target tenant & scope by content_hash and deletion bucket.
		var existingID uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT id
			FROM memories
			WHERE tenant_id = $1
			  AND scope = $2
			  AND project_id IS NOT DISTINCT FROM $3
			  AND content_hash = $4
			  AND (deleted_at IS NULL) = $5
			ORDER BY created_at ASC, id ASC
			LIMIT 1
		`, targetTID, m.Scope, targetPID, m.ContentHash, m.DeletedAt == nil).Scan(&existingID)
		if err == nil {
			memoryIDMap[m.ID] = existingID
			counts.MemoriesSkipped++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return counts, apperror.Wrap(apperror.CodeInternalError, "check duplicate memory", err)
		}

		newMemID, err := allocateUnusedUUID(ctx, tx, "memories", m.ID)
		if err != nil {
			return counts, err
		}
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
		`, newMemID, targetTID, m.Scope, targetPID, m.Category, m.Title, m.ContentMD,
			tags, refs, m.IsPinned, m.Status, m.Version,
			m.ContentHash, actorID, actorID, m.CreatedAt, m.UpdatedAt, m.DeletedAt); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "import memory "+m.ID.String(), err)
		}
		memoryIDMap[m.ID] = newMemID
		importedSet[m.ID] = true
		counts.MemoriesImported++

		for _, rev := range revsByMem[m.ID] {
			snap := rewriteRevisionSnapshot(rev.Snapshot, newMemID, targetTID, targetPID, actorID)
			reqID := rev.RequestID
			if reqID == "" {
				reqID = "import-" + uuid.NewString()
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO memory_revisions (tenant_id, memory_id, version, operation, snapshot, actor_id, request_id, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			`, targetTID, newMemID, rev.Version, rev.Operation, snap, actorID, reqID, rev.CreatedAt); err != nil {
				return counts, apperror.Wrap(apperror.CodeInvalidArgument, "import memory_revision", err)
			}
			counts.RevisionsImported++
		}

		op := "create"
		if m.DeletedAt != nil {
			op = "delete"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO memory_events (tenant_id, project_id, memory_id, operation, memory_version, actor_id)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, targetTID, targetPID, newMemID, op, m.Version, actorID); err != nil {
			return counts, apperror.Wrap(apperror.CodeInternalError, "emit import memory_event", err)
		}
		counts.MemoryEvents++
	}

	for _, m := range p.Memories {
		if !importedSet[m.ID] || m.SourceMemoryID == nil {
			continue
		}
		mappedSrc, ok := memoryIDMap[*m.SourceMemoryID]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE memories SET source_memory_id = $2 WHERE id = $1`, memoryIDMap[m.ID], mappedSrc); err != nil {
			return counts, apperror.Wrap(apperror.CodeInvalidArgument, "backfill source_memory_id", err)
		}
	}

	counts.Memories = counts.MemoriesImported
	counts.MemoryRevisions = counts.RevisionsImported

	if err := tx.Commit(ctx); err != nil {
		return counts, apperror.Wrap(apperror.CodeInternalError, "commit knowledge import tx", err)
	}
	return counts, nil
}

func validateKnowledgeBundle(p KnowledgeBundlePayload) error {
	tenants := make(map[uuid.UUID]bool, len(p.Tenants))
	for _, t := range p.Tenants {
		tenants[t.ID] = true
	}
	projects := make(map[uuid.UUID]uuid.UUID, len(p.Projects))
	for _, proj := range p.Projects {
		if !tenants[proj.TenantID] {
			return apperror.New(apperror.CodeInvalidArgument, "knowledge bundle project references unknown tenant")
		}
		projects[proj.ID] = proj.TenantID
	}
	for _, repo := range p.ProjectRepositories {
		if !tenants[repo.TenantID] {
			return apperror.New(apperror.CodeInvalidArgument, "knowledge bundle repository references unknown tenant")
		}
		if pTid, ok := projects[repo.ProjectID]; !ok || pTid != repo.TenantID {
			return apperror.New(apperror.CodeInvalidArgument, "knowledge bundle repository references invalid project")
		}
	}
	for _, m := range p.Memories {
		if !tenants[m.TenantID] {
			return apperror.New(apperror.CodeInvalidArgument, "knowledge bundle memory references unknown tenant")
		}
		if m.Scope == "project" {
			if m.ProjectID == nil {
				return apperror.New(apperror.CodeInvalidArgument, "project memory missing project_id")
			}
			if pTid, ok := projects[*m.ProjectID]; !ok || pTid != m.TenantID {
				return apperror.New(apperror.CodeInvalidArgument, "knowledge bundle memory references invalid project")
			}
		}
	}
	return nil
}

func resolveKnowledgeTenants(
	ctx context.Context,
	tx pgx.Tx,
	srcTenants []snapshotTenant,
	mode string,
	opts KnowledgeImportOptions,
) (tenantMap map[uuid.UUID]uuid.UUID, matched map[uuid.UUID]bool, created map[uuid.UUID]bool, err error) {
	tenantMap = make(map[uuid.UUID]uuid.UUID, len(srcTenants))
	matched = make(map[uuid.UUID]bool)
	created = make(map[uuid.UUID]bool)

	if mode == "single" {
		if opts.TargetTenantID == uuid.Nil {
			return nil, nil, nil, apperror.New(apperror.CodeInvalidArgument, "请选择要导入的目标租户")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1)`, opts.TargetTenantID).Scan(&exists); err != nil {
			return nil, nil, nil, apperror.Wrap(apperror.CodeInternalError, "check target tenant", err)
		}
		if !exists {
			return nil, nil, nil, apperror.New(apperror.CodeInvalidArgument, "选中的目标租户不存在，请刷新后重试")
		}
		for _, src := range srcTenants {
			tenantMap[src.ID] = opts.TargetTenantID
		}
		if len(srcTenants) > 0 {
			matched[opts.TargetTenantID] = true
		}
		return tenantMap, matched, created, nil
	}

	for _, src := range srcTenants {
		if mappedStr, ok := opts.TenantMap[src.ID.String()]; ok && strings.TrimSpace(mappedStr) != "" && mappedStr != "auto" {
			targetID, parseErr := uuid.Parse(strings.TrimSpace(mappedStr))
			if parseErr != nil {
				return nil, nil, nil, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_map target id")
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1)`, targetID).Scan(&exists); err != nil {
				return nil, nil, nil, apperror.Wrap(apperror.CodeInternalError, "check mapped tenant", err)
			}
			if !exists {
				return nil, nil, nil, apperror.New(apperror.CodeInvalidArgument, "映射的目标租户不存在")
			}
			tenantMap[src.ID] = targetID
			matched[targetID] = true
			continue
		}

		var existingID uuid.UUID
		qErr := tx.QueryRow(ctx, `
			SELECT id FROM tenants
			WHERE name = $1
			ORDER BY created_at ASC, id ASC
			LIMIT 1
		`, src.Name).Scan(&existingID)
		if qErr == nil {
			tenantMap[src.ID] = existingID
			matched[existingID] = true
			continue
		}
		if !errors.Is(qErr, pgx.ErrNoRows) {
			return nil, nil, nil, apperror.Wrap(apperror.CodeInternalError, "lookup tenant by name", qErr)
		}

		newID, allocErr := allocateUnusedUUID(ctx, tx, "tenants", src.ID)
		if allocErr != nil {
			return nil, nil, nil, allocErr
		}
		status := src.Status
		if status != "active" && status != "disabled" {
			status = "active"
		}
		createdAt := src.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tenants (id, name, status, created_at)
			VALUES ($1, $2, $3, $4)
		`, newID, src.Name, status, createdAt); err != nil {
			return nil, nil, nil, apperror.Wrap(apperror.CodeInvalidArgument, "create tenant "+src.Name, err)
		}
		tenantMap[src.ID] = newID
		created[newID] = true
	}
	return tenantMap, matched, created, nil
}

func resolveKnowledgeProjects(
	ctx context.Context,
	tx pgx.Tx,
	srcProjects []snapshotProject,
	reposByProj map[uuid.UUID][]snapshotProjectRepository,
	tenantIDMap map[uuid.UUID]uuid.UUID,
) (projMap map[uuid.UUID]uuid.UUID, matched map[uuid.UUID]bool, created map[uuid.UUID]bool, repoInserted int, err error) {
	projMap = make(map[uuid.UUID]uuid.UUID, len(srcProjects))
	matched = make(map[uuid.UUID]bool)
	created = make(map[uuid.UUID]bool)

	for _, proj := range srcProjects {
		targetTID := tenantIDMap[proj.TenantID]
		repos := reposByProj[proj.ID]
		var targetPID uuid.UUID

		// 1. Match by marker within target tenant.
		if proj.Marker != nil {
			qErr := tx.QueryRow(ctx, `
				SELECT id FROM projects WHERE tenant_id = $1 AND marker = $2 LIMIT 1
			`, targetTID, *proj.Marker).Scan(&targetPID)
			if qErr != nil && !errors.Is(qErr, pgx.ErrNoRows) {
				return nil, nil, nil, 0, apperror.Wrap(apperror.CodeInternalError, "match project by marker", qErr)
			}
		}

		// 2. Match by repository remote + workspace_path within target tenant.
		if targetPID == uuid.Nil {
			for _, repo := range repos {
				qErr := tx.QueryRow(ctx, `
					SELECT project_id FROM project_repositories
					WHERE tenant_id = $1 AND canonical_remote = $2 AND workspace_path = $3
					LIMIT 1
				`, targetTID, repo.CanonicalRemote, repo.WorkspacePath).Scan(&targetPID)
				if qErr == nil {
					break
				}
				if !errors.Is(qErr, pgx.ErrNoRows) {
					return nil, nil, nil, 0, apperror.Wrap(apperror.CodeInternalError, "match project by repository", qErr)
				}
			}
		}

		// 3. Match by name within target tenant.
		if targetPID == uuid.Nil {
			qErr := tx.QueryRow(ctx, `
				SELECT id FROM projects
				WHERE tenant_id = $1 AND name = $2
				ORDER BY (status = 'active') DESC, created_at ASC, id ASC
				LIMIT 1
			`, targetTID, proj.Name).Scan(&targetPID)
			if qErr != nil && !errors.Is(qErr, pgx.ErrNoRows) {
				return nil, nil, nil, 0, apperror.Wrap(apperror.CodeInternalError, "match project by name", qErr)
			}
		}

		if targetPID != uuid.Nil {
			matched[targetPID] = true
		} else {
			newPID, allocErr := allocateUnusedUUID(ctx, tx, "projects", proj.ID)
			if allocErr != nil {
				return nil, nil, nil, 0, allocErr
			}
			marker := proj.Marker
			if marker != nil {
				var markerUsed bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE tenant_id = $1 AND marker = $2)`, targetTID, *marker).Scan(&markerUsed); err != nil {
					return nil, nil, nil, 0, apperror.Wrap(apperror.CodeInternalError, "check project marker", err)
				}
				if markerUsed {
					marker = nil
				}
			}
			status := proj.Status
			if status != "active" && status != "disabled" {
				status = "active"
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO projects (id, tenant_id, name, marker, status, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
			`, newPID, targetTID, proj.Name, marker, status, proj.CreatedAt, proj.UpdatedAt); err != nil {
				return nil, nil, nil, 0, apperror.Wrap(apperror.CodeInvalidArgument, "create project "+proj.Name, err)
			}
			targetPID = newPID
			created[targetPID] = true
		}

		projMap[proj.ID] = targetPID

		for _, repo := range repos {
			newRepoID, allocErr := allocateUnusedUUID(ctx, tx, "project_repositories", repo.ID)
			if allocErr != nil {
				return nil, nil, nil, 0, allocErr
			}
			tag, execErr := tx.Exec(ctx, `
				INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary, created_at)
				VALUES (
					$1, $2, $3, $4, $5,
					$6 AND NOT EXISTS (SELECT 1 FROM project_repositories WHERE project_id = $3 AND is_primary),
					$7
				)
				ON CONFLICT (tenant_id, canonical_remote, workspace_path) DO NOTHING
			`, newRepoID, targetTID, targetPID, repo.CanonicalRemote, repo.WorkspacePath, repo.IsPrimary, repo.CreatedAt)
			if execErr != nil {
				return nil, nil, nil, 0, apperror.Wrap(apperror.CodeInvalidArgument, "bind project_repository", execErr)
			}
			if tag.RowsAffected() > 0 {
				repoInserted++
			}
		}
	}
	return projMap, matched, created, repoInserted, nil
}

func allocateUnusedUUID(ctx context.Context, tx pgx.Tx, table string, preferred uuid.UUID) (uuid.UUID, error) {
	if preferred != uuid.Nil {
		var exists bool
		// table is always an internal constant from callers above.
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, preferred).Scan(&exists); err != nil {
			return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "check uuid availability on "+table, err)
		}
		if !exists {
			return preferred, nil
		}
	}
	return uuid.New(), nil
}

// ensureAdminActorForTenantTx validates an active tenant before resolving the console audit identity.
func ensureAdminActorForTenantTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (uuid.UUID, error) {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1 AND status = 'active')`, tenantID).Scan(&active); err != nil {
		return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "check console actor tenant", err)
	}
	if !active {
		return uuid.Nil, apperror.New(apperror.CodeInvalidArgument, "active tenant required")
	}
	return ensureConsoleAuditActorTx(ctx, tx)
}

// ensureConsoleAuditActorTx resolves only the audit identity, without granting access or changing tenant state.
func ensureConsoleAuditActorTx(ctx context.Context, tx pgx.Tx) (uuid.UUID, error) {
	var actorID uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO principals (id, identity_provider, subject, display_name, status)
		VALUES ($1, 'system_webui', 'admin', 'Web UI System Admin', 'active')
		ON CONFLICT (identity_provider, subject) DO UPDATE SET status = 'active'
		RETURNING id
	`, uuid.New()).Scan(&actorID)
	if err != nil {
		return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "ensure system admin principal", err)
	}
	return actorID, nil
}

func rewriteRevisionSnapshot(raw json.RawMessage, memID, tenantID uuid.UUID, projectID *uuid.UUID, actorID uuid.UUID) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw
	}
	if _, ok := obj["id"]; ok {
		obj["id"] = memID
	}
	if _, ok := obj["tenant_id"]; ok {
		obj["tenant_id"] = tenantID
	}
	if projectID != nil {
		obj["project_id"] = *projectID
	} else {
		delete(obj, "project_id")
	}
	if _, ok := obj["created_by"]; ok {
		obj["created_by"] = actorID
	}
	if _, ok := obj["updated_by"]; ok {
		obj["updated_by"] = actorID
	}
	updated, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return updated
}

// EncodeSnapshotArchive packages SnapshotPayload and its manifest into a ZIP archive.
// When passphrase is non-empty, payload JSON is encrypted with scrypt + AES-256-GCM.
func EncodeSnapshotArchive(payload SnapshotPayload, passphrase string, exportedAt time.Time) ([]byte, SnapshotManifest, error) {
	counts := SnapshotCounts{
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
	}
	return encodeArchive(SnapshotFormat, SnapshotVersion, counts, payload, passphrase, exportedAt)
}

// EncodeKnowledgeArchive packages KnowledgeBundlePayload and its manifest into a ZIP archive.
// When passphrase is non-empty, payload JSON is encrypted with scrypt + AES-256-GCM.
func EncodeKnowledgeArchive(payload KnowledgeBundlePayload, passphrase string, exportedAt time.Time) ([]byte, SnapshotManifest, error) {
	counts := SnapshotCounts{
		Tenants:             len(payload.Tenants),
		Projects:            len(payload.Projects),
		ProjectRepositories: len(payload.ProjectRepositories),
		Memories:            len(payload.Memories),
		MemoryRevisions:     len(payload.MemoryRevisions),
	}
	return encodeArchive(KnowledgeBundleFormat, KnowledgeBundleVersion, counts, payload, passphrase, exportedAt)
}

func encodeArchive(format string, version int, counts SnapshotCounts, payload any, passphrase string, exportedAt time.Time) ([]byte, SnapshotManifest, error) {
	plainJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, SnapshotManifest{}, err
	}
	sum := sha256.Sum256(plainJSON)
	manifest := SnapshotManifest{
		Format:          format,
		SnapshotVersion: version,
		ExportedAt:      exportedAt.UTC(),
		Encrypted:       passphrase != "",
		PayloadSHA256:   hex.EncodeToString(sum[:]),
		Counts:          counts,
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

// InspectArchiveManifest reads and validates only manifest.json from a migration ZIP archive.
func InspectArchiveManifest(zipBytes []byte) (SnapshotManifest, error) {
	manifest, _, err := openArchiveManifest(zipBytes)
	return manifest, err
}

func openArchiveManifest(zipBytes []byte) (SnapshotManifest, map[string]*zip.File, error) {
	var manifest SnapshotManifest
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return manifest, nil, apperror.New(apperror.CodeInvalidArgument, "invalid zip snapshot archive")
	}

	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	mf, ok := files["manifest.json"]
	if !ok {
		return manifest, nil, apperror.New(apperror.CodeInvalidArgument, "snapshot archive missing manifest.json")
	}
	manifestBytes, err := readZipEntryLimited(mf, 1<<20)
	if err != nil {
		return manifest, nil, err
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return manifest, nil, apperror.New(apperror.CodeInvalidArgument, "invalid manifest.json")
	}
	switch manifest.Format {
	case SnapshotFormat:
		if manifest.SnapshotVersion != SnapshotVersion {
			return manifest, nil, apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("unsupported snapshot_version: %d", manifest.SnapshotVersion))
		}
	case KnowledgeBundleFormat:
		if manifest.SnapshotVersion != KnowledgeBundleVersion {
			return manifest, nil, apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("unsupported snapshot_version: %d", manifest.SnapshotVersion))
		}
	default:
		return manifest, nil, apperror.New(apperror.CodeInvalidArgument, "unsupported snapshot format")
	}
	return manifest, files, nil
}

// DecodeSnapshotArchive validates and extracts a full-system migration ZIP archive.
func DecodeSnapshotArchive(zipBytes []byte, passphrase string) (SnapshotManifest, SnapshotPayload, error) {
	decoded, err := DecodeMigrationArchive(zipBytes, passphrase)
	if err != nil {
		return decoded.Manifest, SnapshotPayload{}, err
	}
	if decoded.Manifest.Format != SnapshotFormat || decoded.SystemPayload == nil {
		return decoded.Manifest, SnapshotPayload{}, apperror.New(apperror.CodeInvalidArgument, "unsupported snapshot format")
	}
	return decoded.Manifest, *decoded.SystemPayload, nil
}

// DecodeMigrationArchive validates and extracts either a full-system snapshot or a knowledge bundle.
func DecodeMigrationArchive(zipBytes []byte, passphrase string) (DecodedArchive, error) {
	var out DecodedArchive
	manifest, files, err := openArchiveManifest(zipBytes)
	if err != nil {
		return out, err
	}
	out.Manifest = manifest

	var plainJSON []byte
	if manifest.Encrypted {
		if passphrase == "" {
			return out, apperror.New(apperror.CodeInvalidArgument, "该迁移包已加密，请输入导出时设置的加密口令")
		}
		ef, ok := files["payload.enc"]
		if !ok {
			return out, apperror.New(apperror.CodeInvalidArgument, "encrypted snapshot archive missing payload.enc")
		}
		cipherBytes, err := readZipEntryLimited(ef, maxImportBytes)
		if err != nil {
			return out, err
		}
		plainJSON, err = unsealSnapshotPayload(cipherBytes, passphrase, manifest.KDFSalt)
		if err != nil {
			return out, apperror.New(apperror.CodeInvalidArgument, "解密失败：口令错误或迁移包已损坏")
		}
	} else {
		pf, ok := files["payload.json"]
		if !ok {
			return out, apperror.New(apperror.CodeInvalidArgument, "snapshot archive missing payload.json")
		}
		plainJSON, err = readZipEntryLimited(pf, maxImportBytes)
		if err != nil {
			return out, err
		}
	}

	sum := sha256.Sum256(plainJSON)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), manifest.PayloadSHA256) {
		return out, apperror.New(apperror.CodeInvalidArgument, "快照校验失败：payload SHA-256 不匹配")
	}

	switch manifest.Format {
	case SnapshotFormat:
		var p SnapshotPayload
		if err := json.Unmarshal(plainJSON, &p); err != nil {
			return out, apperror.New(apperror.CodeInvalidArgument, "invalid snapshot payload json")
		}
		out.SystemPayload = &p
	case KnowledgeBundleFormat:
		var b KnowledgeBundlePayload
		if err := json.Unmarshal(plainJSON, &b); err != nil {
			return out, apperror.New(apperror.CodeInvalidArgument, "invalid knowledge bundle payload json")
		}
		out.KnowledgeBundle = &b
	}
	return out, nil
}

func summarizeArchiveTenants(decoded DecodedArchive) []InspectTenantSummary {
	projCounts := make(map[uuid.UUID]int)
	memCounts := make(map[uuid.UUID]int)
	var tenants []snapshotTenant

	if decoded.SystemPayload != nil {
		tenants = decoded.SystemPayload.Tenants
		for _, proj := range decoded.SystemPayload.Projects {
			projCounts[proj.TenantID]++
		}
		for _, m := range decoded.SystemPayload.Memories {
			memCounts[m.TenantID]++
		}
	} else if decoded.KnowledgeBundle != nil {
		tenants = decoded.KnowledgeBundle.Tenants
		for _, proj := range decoded.KnowledgeBundle.Projects {
			projCounts[proj.TenantID]++
		}
		for _, m := range decoded.KnowledgeBundle.Memories {
			memCounts[m.TenantID]++
		}
	}

	out := make([]InspectTenantSummary, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, InspectTenantSummary{
			ID:            t.ID,
			Name:          t.Name,
			Status:        t.Status,
			ProjectsCount: projCounts[t.ID],
			MemoriesCount: memCounts[t.ID],
		})
	}
	return out
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

// normalizeSnapshotPermissions applies the same least-privilege conversion as migration 005.
func normalizeSnapshotPermissions(p SnapshotPayload) SnapshotPayload {
	type membershipKey struct{ tenant, principal uuid.UUID }
	admins := map[membershipKey]bool{}
	system := map[uuid.UUID]bool{}
	projectTenants := map[uuid.UUID]uuid.UUID{}
	for _, pr := range p.Principals {
		if pr.IdentityProvider == "system_webui" {
			system[pr.ID] = true
		}
	}
	for _, proj := range p.Projects {
		projectTenants[proj.ID] = proj.TenantID
	}
	members := make([]snapshotTenantMember, 0, len(p.TenantMembers))
	for _, tm := range p.TenantMembers {
		if tm.Role == "admin" {
			admins[membershipKey{tm.TenantID, tm.PrincipalID}] = true
			tm.Role = "none"
		}
		if !system[tm.PrincipalID] {
			members = append(members, tm)
		}
	}
	p.TenantMembers = members
	grants := make([]snapshotProjectMember, 0, len(p.ProjectMembers))
	for _, pm := range p.ProjectMembers {
		if system[pm.PrincipalID] || admins[membershipKey{projectTenants[pm.ProjectID], pm.PrincipalID}] {
			continue
		}
		if pm.Role == "maintainer" {
			pm.Role = "writer"
		}
		grants = append(grants, pm)
	}
	p.ProjectMembers = grants
	tokens := append([]snapshotAPIToken(nil), p.APITokens...)
	for i := range tokens {
		if system[tokens[i].PrincipalID] && tokens[i].RevokedAt == nil {
			now := time.Now().UTC()
			tokens[i].RevokedAt = &now
		}
	}
	p.APITokens = tokens
	return p
}
