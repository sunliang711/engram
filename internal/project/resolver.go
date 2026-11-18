package project

import (
	"context"
	"errors"

	"engram/internal/apperror"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RemoteInput represents a Git remote entry reported by the client.
type RemoteInput struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// DiscoverInput contains the repository identity sent to memory_discover.
type DiscoverInput struct {
	ProjectMarker   string        `json:"project_marker"`
	MarkerPath      string        `json:"marker_path"`
	Remotes         []RemoteInput `json:"remotes"`
	WorkspacePath   string        `json:"workspace_path"`
	AllowGlobalOnly bool          `json:"allow_global_only"`
}

// ResolvedProject represents a project matched by Resolver.
type ResolvedProject struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	Name          string     `json:"name"`
	Marker        *uuid.UUID `json:"marker,omitempty"`
	MatchedBy     string     `json:"matched_by"`
	WorkspacePath string     `json:"workspace_path,omitempty"`
}

type Resolver struct {
	pool *pgxpool.Pool
}

func NewResolver(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool}
}

type repoCandidate struct {
	projectID       uuid.UUID
	projectName     string
	projectMarker   *uuid.UUID
	canonicalRemote string
	workspacePath   string
	isPrimary       bool
}

// Resolve identifies the project within tenantID according to Section 5.1 rules.
func (r *Resolver) Resolve(ctx context.Context, tenantID uuid.UUID, in DiscoverInput) (*ResolvedProject, error) {
	wsPath, err := NormalizeRelPath(in.WorkspacePath)
	if err != nil {
		return nil, err
	}
	markerPath, err := NormalizeRelPath(in.MarkerPath)
	if err != nil {
		return nil, err
	}
	// If marker_path is provided, it must be an ancestor or equal to workspace_path when workspace_path is set.
	if markerPath != "" && wsPath != "" && !PathMatchesPrefix(wsPath, markerPath) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "marker_path must be an ancestor of or equal to workspace_path")
	}

	var markerProj *ResolvedProject
	if in.ProjectMarker != "" {
		markerUUID, err := uuid.Parse(in.ProjectMarker)
		if err != nil {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid project_marker uuid")
		}
		var p ResolvedProject
		err = r.pool.QueryRow(ctx, `
			SELECT id, tenant_id, name, marker
			FROM projects
			WHERE tenant_id = $1 AND marker = $2 AND status = 'active'
		`, tenantID, markerUUID).Scan(&p.ID, &p.TenantID, &p.Name, &p.Marker)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, apperror.Wrap(apperror.CodeInternalError, "query project by marker", err)
		}
		if err == nil {
			p.MatchedBy = "marker"
			p.WorkspacePath = markerPath
			markerProj = &p
		}
	}

	var remoteProj *ResolvedProject
	if len(in.Remotes) > 0 {
		canonicalList := make([]string, 0, len(in.Remotes))
		seen := make(map[string]bool, len(in.Remotes))
		var firstNormErr error
		for _, rem := range in.Remotes {
			canon, err := NormalizeRemote(rem.URL)
			if err != nil {
				if firstNormErr == nil {
					firstNormErr = err
				}
				continue
			}
			if !seen[canon] {
				seen[canon] = true
				canonicalList = append(canonicalList, canon)
			}
		}
		if len(canonicalList) == 0 && markerProj == nil && in.ProjectMarker == "" && firstNormErr != nil {
			return nil, firstNormErr
		}

		if len(canonicalList) > 0 {
			rows, err := r.pool.Query(ctx, `
				SELECT p.id, p.name, p.marker, pr.canonical_remote, pr.workspace_path, pr.is_primary
				FROM project_repositories pr
				JOIN projects p ON p.id = pr.project_id
				WHERE pr.tenant_id = $1
				  AND pr.canonical_remote = ANY($2)
				  AND p.status = 'active'
			`, tenantID, canonicalList)
			if err != nil {
				return nil, apperror.Wrap(apperror.CodeInternalError, "query project repositories", err)
			}
			defer rows.Close()

			var matched []repoCandidate
			for rows.Next() {
				var c repoCandidate
				if err := rows.Scan(&c.projectID, &c.projectName, &c.projectMarker, &c.canonicalRemote, &c.workspacePath, &c.isPrimary); err != nil {
					return nil, apperror.Wrap(apperror.CodeInternalError, "scan project repository", err)
				}
				if PathMatchesPrefix(wsPath, c.workspacePath) {
					matched = append(matched, c)
				}
			}
			if err := rows.Err(); err != nil {
				return nil, apperror.Wrap(apperror.CodeInternalError, "iterate project repositories", err)
			}

			if len(matched) > 0 {
				// Find longest workspace_path prefix match (Rule 3)
				bestDepth := -1
				bestLen := -1
				for _, c := range matched {
					d := PathDepth(c.workspacePath)
					l := len(c.workspacePath)
					if d > bestDepth || (d == bestDepth && l > bestLen) {
						bestDepth = d
						bestLen = l
					}
				}

				byProject := make(map[uuid.UUID]repoCandidate)
				for _, c := range matched {
					if PathDepth(c.workspacePath) == bestDepth && len(c.workspacePath) == bestLen {
						existing, ok := byProject[c.projectID]
						if !ok || (!existing.isPrimary && c.isPrimary) {
							byProject[c.projectID] = c
						}
					}
				}

				if len(byProject) > 1 {
					return nil, apperror.New(apperror.CodeProjectAmbiguous, "multiple registered projects match repository identity at the same specificity")
				}
				for _, winner := range byProject {
					remoteProj = &ResolvedProject{
						ID:            winner.projectID,
						TenantID:      tenantID,
						Name:          winner.projectName,
						Marker:        winner.projectMarker,
						MatchedBy:     "remote",
						WorkspacePath: winner.workspacePath,
					}
				}
			}
		}
	}

	// Combine markerProj and remoteProj according to Section 5.1 Rules 2, 3, 4:
	// Rule 2: Subproject Marker (markerPath != "") takes priority over Root Marker, and compares specificity with remote workspace_path.
	// Rule 4: Git root Marker (markerPath == "") is only a fallback and cannot override a registered more specific workspace_path.
	if markerProj != nil && markerPath != "" {
		if remoteProj != nil && PathDepth(remoteProj.WorkspacePath) > PathDepth(markerPath) {
			return remoteProj, nil
		}
		return markerProj, nil
	}

	if remoteProj != nil && remoteProj.WorkspacePath != "" {
		return remoteProj, nil
	}

	if markerProj != nil {
		return markerProj, nil
	}

	if remoteProj != nil {
		return remoteProj, nil
	}

	return nil, apperror.New(apperror.CodeProjectNotRegistered, "project is not registered")
}
