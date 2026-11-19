package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"engram/internal/apperror"
	"engram/internal/auth"
	"engram/internal/project"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	maxRegistrationRemotes = 32
	maxProjectNameChars    = 200
)

// RegisterProjectRequest identifies an unregistered repository inside the authenticated tenant.
type RegisterProjectRequest struct {
	Name           string                `json:"name"`
	Remotes        []project.RemoteInput `json:"remotes"`
	WorkspacePath  string                `json:"workspace_path"`
	ProjectMarker  string                `json:"project_marker"`
	IdempotencyKey string                `json:"idempotency_key"`
}

// RegisterProjectResponse returns the new identity; callers rediscover it before writing memories.
type RegisterProjectResponse struct {
	Project project.ResolvedProject `json:"project"`
}

// RegisterProject atomically creates a project, its repository mappings, and the caller's writer grant.
func (s *Service) RegisterProject(ctx context.Context, req RegisterProjectRequest) (*RegisterProjectResponse, error) {
	p, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	p, err = s.auth.VerifyPrincipalMembership(ctx, p.TenantID, p.PrincipalID)
	if err != nil {
		return nil, err
	}
	if !p.CanCreateProject {
		return nil, apperror.New(apperror.CodeUnauthorized, "project creation permission required")
	}
	if err := s.rateLimiter.Allow(p.PrincipalID); err != nil {
		return nil, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > maxProjectNameChars {
		return nil, apperror.New(apperror.CodeInvalidArgument, "name is required and must be <= 200 chars")
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 128 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "idempotency_key is required and must be <= 128 chars")
	}
	ws, err := project.NormalizeRelPath(req.WorkspacePath)
	if err != nil {
		return nil, err
	}
	remotes, err := normalizeRegistrationRemotes(req.Remotes)
	if err != nil {
		return nil, err
	}
	var marker *uuid.UUID
	if req.ProjectMarker != "" {
		id, err := uuid.Parse(req.ProjectMarker)
		if err != nil || id == uuid.Nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, "project_marker must be a nonzero UUID")
		}
		marker = &id
	}
	if len(remotes) == 0 && marker == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "provide remotes or an existing .engram-project UUID")
	}
	// Only repository mappings persist a workspace path; marker locations are supplied during discovery.
	if len(remotes) == 0 && ws != "" {
		return nil, apperror.New(apperror.CodeInvalidArgument, "workspace_path requires remotes; for marker-only registration omit it and report marker_path during memory_discover")
	}
	reqHash := computeRequestHash("project_register", map[string]any{
		"name": req.Name, "remotes": remotes, "workspace_path": ws, "project_marker": marker,
	})
	var resp RegisterProjectResponse
	err = s.runIdempotentWriteTx(ctx, &verifiedAccess{principal: p}, "project_register", req.IdempotencyKey, reqHash, &resp, func(tx pgx.Tx) error {
		// Serialize registrations across members and instances before checking overlapping identities.
		if err := project.LockRegistration(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := authorizeProjectCreation(ctx, tx, p); err != nil {
			return err
		}
		// Include disabled projects and both ancestor directions to prevent identity takeover or shadowing.
		var exists bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM projects WHERE tenant_id = $1 AND (name = $2 OR marker = $3)
				UNION ALL
				SELECT 1 FROM project_repositories
				WHERE tenant_id = $1 AND canonical_remote = ANY($4::text[])
				AND (workspace_path = $5 OR workspace_path = '' OR $5 = ''
					OR starts_with($5, workspace_path || '/') OR starts_with(workspace_path, $5 || '/'))
			)
		`, p.TenantID, req.Name, marker, remotes, ws).Scan(&exists)
		if err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "check registered project identity", err)
		}
		if exists {
			return apperror.New(apperror.CodeProjectExists, "project identity or name is already registered; use memory_discover or contact an administrator")
		}
		if marker == nil {
			id := uuid.New()
			marker = &id
		}
		id := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO projects(id, tenant_id, name, marker) VALUES($1,$2,$3,$4)`, id, p.TenantID, req.Name, marker); err != nil {
			return registrationWriteError(err)
		}
		for i, remote := range remotes {
			if _, err := tx.Exec(ctx, `INSERT INTO project_repositories(id,tenant_id,project_id,canonical_remote,workspace_path,is_primary) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), p.TenantID, id, remote, ws, i == 0); err != nil {
				return registrationWriteError(err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_members(project_id,principal_id,role) VALUES($1,$2,'writer')`, id, p.PrincipalID); err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "grant new project writer permission", err)
		}
		resp.Project = project.ResolvedProject{ID: id, TenantID: p.TenantID, Name: req.Name, Marker: marker, WorkspacePath: ws, MatchedBy: "registration"}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// normalizeRegistrationRemotes validates all aliases and produces a stable identity for retries.
func normalizeRegistrationRemotes(remotes []project.RemoteInput) ([]string, error) {
	if len(remotes) > maxRegistrationRemotes {
		return nil, apperror.New(apperror.CodeInvalidArgument, "too many repository remotes")
	}
	canonical := make([]string, 0, len(remotes))
	seen := make(map[string]bool, len(remotes))
	for _, remote := range remotes {
		normalized, err := project.NormalizeRemote(remote.URL)
		if err != nil {
			return nil, err
		}
		if !seen[normalized] {
			seen[normalized] = true
			canonical = append(canonical, normalized)
		}
	}
	sort.Strings(canonical)
	return canonical, nil
}

// authorizeProjectCreation locks live grants so revocation cannot race a registration commit.
func authorizeProjectCreation(ctx context.Context, tx pgx.Tx, p auth.Principal) error {
	var allowed bool
	err := tx.QueryRow(ctx, `
		SELECT tm.can_create_project FROM tenant_members tm
		JOIN tenants t ON t.id = tm.tenant_id
		JOIN principals pr ON pr.id = tm.principal_id
		WHERE tm.tenant_id = $1 AND tm.principal_id = $2
		AND t.status = 'active' AND pr.status = 'active' AND pr.identity_provider <> 'system_webui'
		FOR SHARE OF tm, t, pr
	`, p.TenantID, p.PrincipalID).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return apperror.New(apperror.CodeUnauthorized, "project creation permission required")
	}
	if err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "verify project creation permission", err)
	}
	return nil
}

// authorizeCachedRegistration prevents replay from exposing disabled projects or revoked grants.
func authorizeCachedRegistration(ctx context.Context, tx pgx.Tx, p auth.Principal, raw []byte) error {
	if err := authorizeProjectCreation(ctx, tx, p); err != nil {
		return err
	}
	var cached RegisterProjectResponse
	if err := json.Unmarshal(raw, &cached); err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "decode cached registration", err)
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM projects p JOIN project_members pm ON pm.project_id = p.id
		WHERE p.tenant_id = $1 AND p.id = $2 AND p.status = 'active'
		AND pm.principal_id = $3 AND pm.role = 'writer'
	)`, p.TenantID, cached.Project.ID, p.PrincipalID).Scan(&allowed)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "verify cached registration grant", err)
	}
	if !allowed {
		return apperror.New(apperror.CodeUnauthorized, "new project writer permission is no longer available")
	}
	return nil
}

// registrationWriteError translates concurrent identity collisions without exposing database details.
func registrationWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apperror.New(apperror.CodeProjectExists, "project identity is already registered")
	}
	return apperror.Wrap(apperror.CodeInternalError, "register project identity", err)
}
