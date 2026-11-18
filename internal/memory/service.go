package memory

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"engram/internal/apperror"
	"engram/internal/auth"
	"engram/internal/config"
	"engram/internal/notify"
	"engram/internal/observability"
	"engram/internal/project"
	"engram/internal/security"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// Service implements all Engram Server core operations.
type Service struct {
	pool        *pgxpool.Pool
	cfg         config.Config
	auth        *auth.Authenticator
	signer      *auth.TokenSigner
	rateLimiter *auth.RateLimiter
	resolver    *project.Resolver
	hub         *notify.Hub
	metrics     *observability.Metrics
	log         zerolog.Logger
}

func NewService(
	pool *pgxpool.Pool,
	cfg config.Config,
	authenticator *auth.Authenticator,
	signer *auth.TokenSigner,
	rateLimiter *auth.RateLimiter,
	resolver *project.Resolver,
	hub *notify.Hub,
	metrics *observability.Metrics,
	log zerolog.Logger,
) *Service {
	return &Service{
		pool:        pool,
		cfg:         cfg,
		auth:        authenticator,
		signer:      signer,
		rateLimiter: rateLimiter,
		resolver:    resolver,
		hub:         hub,
		metrics:     metrics,
		log:         log,
	}
}

type verifiedAccess struct {
	principal   auth.Principal
	claims      *auth.ProjectContextClaims
	projectRole string
	perms       PermissionsInfo
}

func (s *Service) verifyAccess(ctx context.Context, projectContextToken string) (*verifiedAccess, error) {
	p, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	// Re-verify live tenant membership on every call (Section 6)
	p, err = s.auth.VerifyPrincipalMembership(ctx, p.TenantID, p.PrincipalID)
	if err != nil {
		return nil, err
	}
	claims, err := s.signer.VerifyProjectContext(projectContextToken, p.TenantID, p.PrincipalID)
	if err != nil {
		return nil, err
	}

	var projRole string
	if claims.Mode == auth.ModeProject && claims.ProjectID != nil {
		projRole, err = s.auth.GetProjectRole(ctx, p.TenantID, *claims.ProjectID, p.PrincipalID, p.TenantRole)
		if err != nil {
			return nil, err
		}
	}

	perms := PermissionsInfo{
		TenantRole:         p.TenantRole,
		ProjectRole:        projRole,
		CanReadGlobal:      auth.CanReadGlobal(p.TenantRole),
		CanWriteGlobal:     auth.CanWriteGlobal(p.TenantRole),
		CanReadProject:     auth.CanReadProject(projRole),
		CanWriteProject:    auth.CanWriteProject(projRole),
		CanMaintainProject: auth.CanMaintainProject(projRole),
	}

	return &verifiedAccess{
		principal:   p,
		claims:      claims,
		projectRole: projRole,
		perms:       perms,
	}, nil
}

// Discover implements Section 7.1 memory_discover.
func (s *Service) Discover(ctx context.Context, req DiscoverRequest) (*DiscoverResponse, error) {
	p, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	p, err = s.auth.VerifyPrincipalMembership(ctx, p.TenantID, p.PrincipalID)
	if err != nil {
		return nil, err
	}

	wsPath, err := project.NormalizeRelPath(req.WorkspacePath)
	if err != nil {
		return nil, err
	}
	markerPath, err := project.NormalizeRelPath(req.MarkerPath)
	if err != nil {
		return nil, err
	}

	var resolvedProj *project.ResolvedProject
	var projRole string
	mode := auth.ModeGlobalOnly

	hasIdentityInput := strings.TrimSpace(req.ProjectMarker) != "" || len(req.Remotes) > 0
	if !hasIdentityInput && (wsPath != "" || markerPath != "") && !req.AllowGlobalOnly {
		return nil, apperror.New(apperror.CodeInvalidArgument, "cannot identify project from workspace_path or marker_path alone; provide remotes or project_marker, or set allow_global_only=true")
	}
	if hasIdentityInput {
		proj, err := s.resolver.Resolve(ctx, p.TenantID, project.DiscoverInput{
			ProjectMarker:   req.ProjectMarker,
			MarkerPath:      req.MarkerPath,
			Remotes:         req.Remotes,
			WorkspacePath:   req.WorkspacePath,
			AllowGlobalOnly: req.AllowGlobalOnly,
		})
		if err != nil {
			var appErr *apperror.Error
			if errors.As(err, &appErr) && appErr.Code == apperror.CodeProjectNotRegistered && req.AllowGlobalOnly {
				resolvedProj = nil
				mode = auth.ModeGlobalOnly
			} else {
				return nil, err
			}
		} else {
			role, err := s.auth.GetProjectRole(ctx, p.TenantID, proj.ID, p.PrincipalID, p.TenantRole)
			if err != nil {
				return nil, err
			}
			if auth.CanReadProject(role) {
				resolvedProj = proj
				projRole = role
				mode = auth.ModeProject
			} else {
				// Valid tenant member without access to this specific project falls back to global_only mode (Section 5.1 Rule 6).
				resolvedProj = nil
				projRole = ""
				mode = auth.ModeGlobalOnly
			}
		}
	}

	var projIDPtr *uuid.UUID
	if resolvedProj != nil {
		id := resolvedProj.ID
		projIDPtr = &id
	}

	ctxToken, err := s.signer.SignProjectContext(p.TenantID, projIDPtr, p.PrincipalID, mode)
	if err != nil {
		return nil, err
	}

	globalIdx, err := s.buildScopeIndex(ctx, p.TenantID, "global", nil)
	if err != nil {
		return nil, err
	}

	var projectIdx *ScopeIndex
	if mode == auth.ModeProject && projIDPtr != nil {
		idx, err := s.buildScopeIndex(ctx, p.TenantID, "project", projIDPtr)
		if err != nil {
			return nil, err
		}
		projectIdx = &idx
	}

	latestCursor, err := s.getLatestCursor(ctx, p.TenantID, projIDPtr)
	if err != nil {
		return nil, err
	}

	return &DiscoverResponse{
		ProjectContext: ctxToken,
		Mode:           mode,
		Project:        resolvedProj,
		Permissions: PermissionsInfo{
			TenantRole:         p.TenantRole,
			ProjectRole:        projRole,
			CanReadGlobal:      auth.CanReadGlobal(p.TenantRole),
			CanWriteGlobal:     auth.CanWriteGlobal(p.TenantRole),
			CanReadProject:     auth.CanReadProject(projRole),
			CanWriteProject:    auth.CanWriteProject(projRole),
			CanMaintainProject: auth.CanMaintainProject(projRole),
		},
		GlobalIndex:  globalIdx,
		ProjectIndex: projectIdx,
		LatestCursor: latestCursor,
	}, nil
}

func (s *Service) buildScopeIndex(ctx context.Context, tenantID uuid.UUID, scope string, projectID *uuid.UUID) (ScopeIndex, error) {
	idx := ScopeIndex{
		Pinned:        []IndexItem{},
		CategoryStats: map[string]int{},
		Recent:        []IndexItem{},
	}

	// 1. Category counts
	catRows, err := s.pool.Query(ctx, `
		SELECT category, count(*)::int
		FROM memories
		WHERE tenant_id = $1
		  AND scope = $2
		  AND ($3::uuid IS NULL OR project_id = $3)
		  AND status = 'active'
		  AND deleted_at IS NULL
		GROUP BY category
	`, tenantID, scope, projectID)
	if err != nil {
		return idx, apperror.Wrap(apperror.CodeInternalError, "query category stats", err)
	}
	defer catRows.Close()
	for catRows.Next() {
		var cat string
		var cnt int
		if err := catRows.Scan(&cat, &cnt); err != nil {
			return idx, apperror.Wrap(apperror.CodeInternalError, "scan category stats", err)
		}
		idx.CategoryStats[cat] = cnt
	}
	if err := catRows.Err(); err != nil {
		return idx, apperror.Wrap(apperror.CodeInternalError, "iterate category stats", err)
	}

	// 2. Pinned items (up to 10)
	pinnedRows, err := s.pool.Query(ctx, `
		SELECT id, category, title, tags, updated_at
		FROM memories
		WHERE tenant_id = $1
		  AND scope = $2
		  AND ($3::uuid IS NULL OR project_id = $3)
		  AND status = 'active'
		  AND is_pinned = true
		  AND deleted_at IS NULL
		ORDER BY updated_at DESC, id ASC
		LIMIT 10
	`, tenantID, scope, projectID)
	if err != nil {
		return idx, apperror.Wrap(apperror.CodeInternalError, "query pinned memories", err)
	}
	defer pinnedRows.Close()
	for pinnedRows.Next() {
		item, err := scanIndexItem(pinnedRows)
		if err != nil {
			return idx, err
		}
		idx.Pinned = append(idx.Pinned, item)
	}
	if err := pinnedRows.Err(); err != nil {
		return idx, apperror.Wrap(apperror.CodeInternalError, "iterate pinned memories", err)
	}

	// 3. Recent non-pinned items (up to 5, avoiding duplication with pinned)
	recentRows, err := s.pool.Query(ctx, `
		SELECT id, category, title, tags, updated_at
		FROM memories
		WHERE tenant_id = $1
		  AND scope = $2
		  AND ($3::uuid IS NULL OR project_id = $3)
		  AND status = 'active'
		  AND is_pinned = false
		  AND deleted_at IS NULL
		ORDER BY updated_at DESC, id ASC
		LIMIT 5
	`, tenantID, scope, projectID)
	if err != nil {
		return idx, apperror.Wrap(apperror.CodeInternalError, "query recent memories", err)
	}
	defer recentRows.Close()
	for recentRows.Next() {
		item, err := scanIndexItem(recentRows)
		if err != nil {
			return idx, err
		}
		idx.Recent = append(idx.Recent, item)
	}
	if err := recentRows.Err(); err != nil {
		return idx, apperror.Wrap(apperror.CodeInternalError, "iterate recent memories", err)
	}

	return idx, nil
}

// Query implements Section 7.2 memory_query and Section 11 Search Design.
func (s *Service) Query(ctx context.Context, req QueryRequest) (*QueryResponse, error) {
	access, err := s.verifyAccess(ctx, req.ProjectContext)
	if err != nil {
		return nil, err
	}

	scopes := req.Scopes
	if len(scopes) == 0 {
		if access.claims.Mode == auth.ModeProject {
			scopes = []string{"global", "project"}
		} else {
			scopes = []string{"global"}
		}
	}

	includeGlobal := false
	includeProject := false
	for _, sc := range scopes {
		switch sc {
		case "global":
			if !access.perms.CanReadGlobal {
				return nil, apperror.New(apperror.CodeUnauthorized, "no permission to read global memory")
			}
			includeGlobal = true
		case "project":
			if access.claims.Mode != auth.ModeProject || access.claims.ProjectID == nil || !access.perms.CanReadProject {
				return nil, apperror.New(apperror.CodeUnauthorized, "no permission to read project memory")
			}
			includeProject = true
		default:
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid scope in scopes")
		}
	}

	statuses := req.Status
	if len(statuses) == 0 {
		statuses = []string{"active"}
	}
	for _, st := range statuses {
		if err := security.ValidateStatus(st); err != nil {
			return nil, err
		}
	}
	for _, cat := range req.Categories {
		if err := security.ValidateCategory(cat); err != nil {
			return nil, err
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = s.cfg.Limits.DefaultQueryLimit
	}
	if limit > s.cfg.Limits.MaxQueryLimit {
		limit = s.cfg.Limits.MaxQueryLimit
	}

	q := strings.TrimSpace(req.Query)

	var qCur queryCursor
	if req.Cursor != nil && strings.TrimSpace(*req.Cursor) != "" {
		parsedCur, err := decodeQueryCursor(strings.TrimSpace(*req.Cursor))
		if err != nil {
			return nil, err
		}
		qCur = parsedCur
	}

	var (
		args  []any
		where []string
	)
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	// Mandatory scope condition (Section 11)
	tenantPlaceholder := addArg(access.principal.TenantID)
	where = append(where, fmt.Sprintf("tenant_id = %s", tenantPlaceholder))
	where = append(where, "deleted_at IS NULL")

	switch {
	case includeGlobal && includeProject:
		projPlaceholder := addArg(*access.claims.ProjectID)
		where = append(where, fmt.Sprintf("(scope = 'global' OR (scope = 'project' AND project_id = %s))", projPlaceholder))
	case includeGlobal:
		where = append(where, "scope = 'global'")
	case includeProject:
		projPlaceholder := addArg(*access.claims.ProjectID)
		where = append(where, fmt.Sprintf("(scope = 'project' AND project_id = %s)", projPlaceholder))
	default:
		return nil, apperror.New(apperror.CodeInvalidArgument, "at least one scope must be specified")
	}

	statusPlaceholder := addArg(statuses)
	where = append(where, fmt.Sprintf("status = ANY(%s)", statusPlaceholder))

	if len(req.Categories) > 0 {
		catPlaceholder := addArg(req.Categories)
		where = append(where, fmt.Sprintf("category = ANY(%s)", catPlaceholder))
	}
	if len(req.Tags) > 0 {
		tagPlaceholder := addArg(req.Tags)
		where = append(where, fmt.Sprintf("tags @> %s", tagPlaceholder))
	}

	orderBy := "is_pinned DESC, updated_at DESC, id ASC"
	sqlOffset := 0
	if q == "" && qCur.IsPinned != nil && qCur.UpdatedAt != nil && qCur.ID != nil {
		// Keyset pagination for empty-query listing to avoid OFFSET drift
		pPin := addArg(*qCur.IsPinned)
		pUpd := addArg(*qCur.UpdatedAt)
		pID := addArg(*qCur.ID)
		where = append(where, fmt.Sprintf(`(
			(is_pinned = false AND %[1]s::boolean = true)
			OR (is_pinned = %[1]s::boolean AND updated_at < %[2]s)
			OR (is_pinned = %[1]s::boolean AND updated_at = %[2]s AND id > %[3]s)
		)`, pPin, pUpd, pID))
	} else {
		sqlOffset = qCur.Offset
	}

	if q != "" {
		qPlaceholder := addArg(q)
		likeEscaped := escapeLike(q)
		subPlaceholder := addArg("%" + likeEscaped + "%")
		prefixPlaceholder := addArg(likeEscaped + "%")
		runeCount := utf8.RuneCountInString(q)

		// Split multi-term queries into individual tokens for substring fallback
		tokens := strings.Fields(q)
		var tokenChecks []string
		for _, tok := range tokens {
			if tok == q {
				continue
			}
			tp := addArg("%" + escapeLike(tok) + "%")
			tokenChecks = append(tokenChecks, fmt.Sprintf("(title ILIKE %s OR content_md ILIKE %s)", tp, tp))
		}
		allTokensExpr := "false"
		if len(tokenChecks) > 0 {
			allTokensExpr = "(" + strings.Join(tokenChecks, " AND ") + ")"
		}

		if runeCount < 3 {
			// Short keyword (< 3 Unicode chars): FTS + exact/prefix + ILIKE substring fallback (Section 11 Step 5)
			where = append(where, fmt.Sprintf(`(
				lower(title) = lower(%s)
				OR title ILIKE %s
				OR content_md ILIKE %s
				OR fts_doc @@ websearch_to_tsquery('english', %s)
				OR %s
			)`, qPlaceholder, subPlaceholder, subPlaceholder, qPlaceholder, allTokensExpr))

			orderBy = fmt.Sprintf(`(
				(CASE WHEN lower(title) = lower(%[1]s) THEN 100.0 ELSE 0.0 END)
				+ (CASE WHEN title ILIKE %[3]s THEN 50.0 ELSE 0.0 END)
				+ (CASE WHEN title ILIKE %[2]s THEN 30.0 ELSE 0.0 END)
				+ (CASE WHEN content_md ILIKE %[2]s THEN 10.0 ELSE 0.0 END)
				+ coalesce(ts_rank_cd(fts_doc, websearch_to_tsquery('english', %[1]s)), 0.0) * 20.0
			) DESC, is_pinned DESC, updated_at DESC, id ASC`, qPlaceholder, subPlaceholder, prefixPlaceholder)
		} else {
			// >= 3 Unicode chars: Title exact/prefix + FTS + pg_trgm similarity + ILIKE fallback (Section 11 Steps 1, 3, 4, 6)
			where = append(where, fmt.Sprintf(`(
				lower(title) = lower(%[1]s)
				OR title ILIKE %[2]s
				OR content_md ILIKE %[2]s
				OR fts_doc @@ websearch_to_tsquery('english', %[1]s)
				OR similarity(title, %[1]s) >= 0.15
				OR word_similarity(%[1]s, content_md) >= 0.25
				OR %[3]s
			)`, qPlaceholder, subPlaceholder, allTokensExpr))

			orderBy = fmt.Sprintf(`(
				(CASE WHEN lower(title) = lower(%[1]s) THEN 100.0 ELSE 0.0 END)
				+ (CASE WHEN title ILIKE %[3]s THEN 50.0 ELSE 0.0 END)
				+ (CASE WHEN title ILIKE %[2]s THEN 25.0 ELSE 0.0 END)
				+ (CASE WHEN content_md ILIKE %[2]s THEN 10.0 ELSE 0.0 END)
				+ coalesce(ts_rank_cd(fts_doc, websearch_to_tsquery('english', %[1]s)), 0.0) * 20.0
				+ similarity(title, %[1]s) * 30.0
				+ word_similarity(%[1]s, content_md) * 15.0
			) DESC, is_pinned DESC, updated_at DESC, id ASC`, qPlaceholder, subPlaceholder, prefixPlaceholder)
		}
	}

	limitPlaceholder := addArg(limit + 1)
	offsetPlaceholder := addArg(sqlOffset)

	sqlStr := fmt.Sprintf(`
		SELECT id, scope, category, title, content_md, tags, is_pinned, status, version, updated_at
		FROM memories
		WHERE %s
		ORDER BY %s
		LIMIT %s OFFSET %s
	`, strings.Join(where, " AND "), orderBy, limitPlaceholder, offsetPlaceholder)

	rows, err := s.pool.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "execute memory query", err)
	}
	defer rows.Close()

	items := make([]MemorySummary, 0, limit)
	for rows.Next() {
		sum, err := scanMemorySummary(rows, q)
		if err != nil {
			return nil, err
		}
		items = append(items, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "iterate memory query rows", err)
	}

	var nextCursor *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		nc := encodeQueryCursor(queryCursor{
			Offset:    qCur.Offset + limit,
			IsPinned:  &last.IsPinned,
			UpdatedAt: &last.UpdatedAt,
			ID:        &last.ID,
		})
		nextCursor = &nc
	}

	if q != "" && s.metrics != nil {
		s.metrics.ObserveSearch(len(items))
	}

	latestCursor, err := s.getLatestCursor(ctx, access.principal.TenantID, access.claims.ProjectID)
	if err != nil {
		return nil, err
	}

	return &QueryResponse{
		Items:        items,
		NextCursor:   nextCursor,
		LatestCursor: latestCursor,
	}, nil
}

// Read implements Section 7.3 memory_read.
func (s *Service) Read(ctx context.Context, req ReadRequest) (*ReadResponse, error) {
	access, err := s.verifyAccess(ctx, req.ProjectContext)
	if err != nil {
		return nil, err
	}
	memID, err := uuid.Parse(strings.TrimSpace(req.MemoryID))
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id uuid")
	}

	detail, err := s.loadMemoryByID(ctx, s.pool, access.principal.TenantID, memID)
	if err != nil {
		return nil, err
	}

	// Anti-probing check: if caller does not have read permission for this memory's scope/project, return MEMORY_NOT_FOUND
	if !s.canReadMemory(access, detail) {
		return nil, apperror.New(apperror.CodeMemoryNotFound, "memory not found")
	}

	if req.Version != nil {
		if *req.Version <= 0 {
			return nil, apperror.New(apperror.CodeInvalidArgument, "version must be positive")
		}
		var snapshotBytes []byte
		err := s.pool.QueryRow(ctx, `
			SELECT snapshot
			FROM memory_revisions
			WHERE tenant_id = $1 AND memory_id = $2 AND version = $3
		`, access.principal.TenantID, memID, *req.Version).Scan(&snapshotBytes)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, apperror.New(apperror.CodeMemoryNotFound, "requested memory version not found")
			}
			return nil, apperror.Wrap(apperror.CodeInternalError, "query memory revision", err)
		}
		var snapDetail MemoryDetail
		if err := json.Unmarshal(snapshotBytes, &snapDetail); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "unmarshal revision snapshot", err)
		}
		latestCursor, err := s.getLatestCursor(ctx, access.principal.TenantID, access.claims.ProjectID)
		if err != nil {
			return nil, err
		}
		return &ReadResponse{
			Memory:       snapDetail,
			LatestCursor: latestCursor,
		}, nil
	}

	if detail.DeletedAt != nil {
		return nil, apperror.New(apperror.CodeMemoryNotFound, "memory not found")
	}

	latestCursor, err := s.getLatestCursor(ctx, access.principal.TenantID, access.claims.ProjectID)
	if err != nil {
		return nil, err
	}

	return &ReadResponse{
		Memory:       *detail,
		LatestCursor: latestCursor,
	}, nil
}

// Create implements Section 7.4 memory_create.
func (s *Service) Create(ctx context.Context, req CreateRequest, reqID string) (*WriteMemoryResponse, error) {
	access, err := s.verifyAccess(ctx, req.ProjectContext)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimiter.Allow(access.principal.PrincipalID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 128 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "idempotency_key is required and must be <= 128 chars")
	}
	if err := security.ValidateScope(req.Scope); err != nil {
		return nil, err
	}
	if err := security.ValidateCategory(req.Category); err != nil {
		return nil, err
	}
	tags := normalizeTags(req.Tags)
	refs := req.SourceRefs
	if refs == nil {
		refs = []security.SourceRef{}
	}
	if err := security.ValidateMemoryContent(s.cfg.Limits, req.Title, req.Content, tags, refs); err != nil {
		return nil, err
	}

	var targetProjectID *uuid.UUID
	if req.Scope == "global" {
		if !access.perms.CanWriteGlobal {
			return nil, apperror.New(apperror.CodeUnauthorized, "global_writer or admin role required to create global memory")
		}
	} else {
		if access.claims.Mode != auth.ModeProject || access.claims.ProjectID == nil || !access.perms.CanWriteProject {
			return nil, apperror.New(apperror.CodeUnauthorized, "writer or maintainer role required to create project memory")
		}
		targetProjectID = access.claims.ProjectID
	}

	reqHash := computeRequestHash("memory_create", map[string]any{
		"scope":       req.Scope,
		"project_id":  targetProjectID,
		"category":    req.Category,
		"title":       strings.TrimSpace(req.Title),
		"content":     req.Content,
		"tags":        tags,
		"is_pinned":   req.IsPinned,
		"source_refs": refs,
	})

	var resp WriteMemoryResponse
	err = s.runIdempotentWriteTx(ctx, access.principal, "memory_create", req.IdempotencyKey, reqHash, &resp, func(tx pgx.Tx) error {
		now := time.Now().UTC()
		memID := uuid.New()
		cHash := security.ContentHash(req.Content)
		refsJSON, err := json.Marshal(refs)
		if err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "marshal source_refs", err)
		}

		detail := MemoryDetail{
			ID:          memID,
			TenantID:    access.principal.TenantID,
			Scope:       req.Scope,
			ProjectID:   targetProjectID,
			Category:    req.Category,
			Title:       strings.TrimSpace(req.Title),
			Content:     req.Content,
			Tags:        tags,
			SourceRefs:  refs,
			IsPinned:    req.IsPinned,
			Status:      "active",
			Version:     1,
			ContentHash: cHash,
			CreatedBy:   access.principal.PrincipalID,
			UpdatedBy:   access.principal.PrincipalID,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO memories (
				id, tenant_id, scope, project_id, category, title, content_md,
				tags, source_refs, is_pinned, status, version, content_hash,
				created_by, updated_by, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12, $13,
				$14, $15, $16, $17
			)
		`,
			detail.ID, detail.TenantID, detail.Scope, detail.ProjectID, detail.Category, detail.Title, detail.Content,
			detail.Tags, refsJSON, detail.IsPinned, detail.Status, detail.Version, detail.ContentHash,
			detail.CreatedBy, detail.UpdatedBy, detail.CreatedAt, detail.UpdatedAt,
		)
		if err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "insert memory", err)
		}

		dups, err := findDuplicateCandidates(ctx, tx, detail.TenantID, detail.Scope, detail.ProjectID, detail.ContentHash, detail.ID)
		if err != nil {
			return err
		}

		if err := insertRevision(ctx, tx, detail, "create", access.principal.PrincipalID, reqID); err != nil {
			return err
		}

		evRev, err := s.appendEventLocked(ctx, tx, detail.TenantID, detail.ProjectID, detail.ID, "create", detail.Version, access.principal.PrincipalID)
		if err != nil {
			return err
		}

		latestCur, err := s.signer.SignCursor(access.principal.TenantID, access.claims.ProjectID, evRev)
		if err != nil {
			return err
		}

		resp = WriteMemoryResponse{
			Memory:              detail,
			DuplicateCandidates: dups,
			LatestCursor:        latestCur,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Update implements Section 7.5 memory_update and Section 9 Optimistic Locking.
func (s *Service) Update(ctx context.Context, req UpdateRequest, reqID string) (*WriteMemoryResponse, error) {
	access, err := s.verifyAccess(ctx, req.ProjectContext)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimiter.Allow(access.principal.PrincipalID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 128 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "idempotency_key is required and must be <= 128 chars")
	}
	memID, err := uuid.Parse(strings.TrimSpace(req.MemoryID))
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id uuid")
	}
	if req.ExpectedVersion <= 0 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "expected_version must be > 0")
	}
	if isPatchEmpty(req.Patch) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "patch must contain at least one field")
	}

	// Pre-validate patch fields before entering DB transaction
	if req.Patch.Category != nil {
		if err := security.ValidateCategory(*req.Patch.Category); err != nil {
			return nil, err
		}
	}
	if req.Patch.Status != nil {
		if err := security.ValidateStatus(*req.Patch.Status); err != nil {
			return nil, err
		}
	}
	if req.Patch.Title != nil {
		t := strings.TrimSpace(*req.Patch.Title)
		if t == "" || len(t) > s.cfg.Limits.MaxTitleBytes {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid title length in patch")
		}
		if err := security.CheckTextSafety(t); err != nil {
			return nil, err
		}
	}
	if req.Patch.Content != nil {
		c := *req.Patch.Content
		if strings.TrimSpace(c) == "" || len(c) > s.cfg.Limits.MaxContentBytes {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid content size in patch")
		}
		if err := security.CheckTextSafety(c); err != nil {
			return nil, err
		}
	}
	if req.Patch.Tags != nil {
		if err := security.ValidateTags(s.cfg.Limits, *req.Patch.Tags); err != nil {
			return nil, err
		}
	}
	if req.Patch.SourceRefs != nil {
		if err := security.ValidateSourceRefs(s.cfg.Limits, *req.Patch.SourceRefs); err != nil {
			return nil, err
		}
	}

	reqHash := computeRequestHash("memory_update", map[string]any{
		"memory_id":        memID,
		"expected_version": req.ExpectedVersion,
		"patch":            req.Patch,
	})

	var resp WriteMemoryResponse
	err = s.runIdempotentWriteTx(ctx, access.principal, "memory_update", req.IdempotencyKey, reqHash, &resp, func(tx pgx.Tx) error {
		current, err := s.loadMemoryForWriteCheck(ctx, tx, access, memID, false)
		if err != nil {
			return err
		}

		// Merge patch onto current values
		updated := *current
		if req.Patch.Category != nil {
			updated.Category = *req.Patch.Category
		}
		if req.Patch.Title != nil {
			updated.Title = strings.TrimSpace(*req.Patch.Title)
		}
		if req.Patch.Content != nil {
			updated.Content = *req.Patch.Content
			updated.ContentHash = security.ContentHash(updated.Content)
		}
		if req.Patch.Tags != nil {
			updated.Tags = normalizeTags(*req.Patch.Tags)
		}
		if req.Patch.SourceRefs != nil {
			updated.SourceRefs = *req.Patch.SourceRefs
		}
		if req.Patch.IsPinned != nil {
			updated.IsPinned = *req.Patch.IsPinned
		}
		if req.Patch.Status != nil {
			updated.Status = *req.Patch.Status
		}
		updated.UpdatedBy = access.principal.PrincipalID
		updated.UpdatedAt = time.Now().UTC()

		refsJSON, err := json.Marshal(updated.SourceRefs)
		if err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "marshal source_refs", err)
		}

		// Conditional SQL update with optimistic lock (Section 9)
		var newVersion int64
		err = tx.QueryRow(ctx, `
			UPDATE memories
			SET category = $1,
				title = $2,
				content_md = $3,
				tags = $4,
				source_refs = $5,
				is_pinned = $6,
				status = $7,
				content_hash = $8,
				version = version + 1,
				updated_by = $9,
				updated_at = $10
			WHERE id = $11
			  AND tenant_id = $12
			  AND version = $13
			  AND deleted_at IS NULL
			RETURNING version
		`,
			updated.Category, updated.Title, updated.Content, updated.Tags,
			refsJSON, updated.IsPinned, updated.Status, updated.ContentHash,
			updated.UpdatedBy, updated.UpdatedAt,
			memID, access.principal.TenantID, req.ExpectedVersion,
		).Scan(&newVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return s.conflictOrNotFoundError(ctx, tx, access, memID)
			}
			return apperror.Wrap(apperror.CodeInternalError, "update memory", err)
		}
		updated.Version = newVersion

		dups, err := findDuplicateCandidates(ctx, tx, updated.TenantID, updated.Scope, updated.ProjectID, updated.ContentHash, updated.ID)
		if err != nil {
			return err
		}

		if err := insertRevision(ctx, tx, updated, "update", access.principal.PrincipalID, reqID); err != nil {
			return err
		}

		evRev, err := s.appendEventLocked(ctx, tx, updated.TenantID, updated.ProjectID, updated.ID, "update", updated.Version, access.principal.PrincipalID)
		if err != nil {
			return err
		}

		latestCur, err := s.signer.SignCursor(access.principal.TenantID, access.claims.ProjectID, evRev)
		if err != nil {
			return err
		}

		resp = WriteMemoryResponse{
			Memory:              updated,
			DuplicateCandidates: dups,
			LatestCursor:        latestCur,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Delete implements Section 7.6 memory_delete (soft delete with optimistic lock).
func (s *Service) Delete(ctx context.Context, req DeleteRequest, reqID string) (*DeleteResponse, error) {
	access, err := s.verifyAccess(ctx, req.ProjectContext)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimiter.Allow(access.principal.PrincipalID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 128 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "idempotency_key is required and must be <= 128 chars")
	}
	memID, err := uuid.Parse(strings.TrimSpace(req.MemoryID))
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id uuid")
	}
	if req.ExpectedVersion <= 0 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "expected_version must be > 0")
	}

	reqHash := computeRequestHash("memory_delete", map[string]any{
		"memory_id":        memID,
		"expected_version": req.ExpectedVersion,
	})

	var resp DeleteResponse
	err = s.runIdempotentWriteTx(ctx, access.principal, "memory_delete", req.IdempotencyKey, reqHash, &resp, func(tx pgx.Tx) error {
		current, err := s.loadMemoryForWriteCheck(ctx, tx, access, memID, true)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		var newVersion int64
		err = tx.QueryRow(ctx, `
			UPDATE memories
			SET deleted_at = $1,
				version = version + 1,
				updated_by = $2,
				updated_at = $1
			WHERE id = $3
			  AND tenant_id = $4
			  AND version = $5
			  AND deleted_at IS NULL
			RETURNING version
		`, now, access.principal.PrincipalID, memID, access.principal.TenantID, req.ExpectedVersion).Scan(&newVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return s.conflictOrNotFoundError(ctx, tx, access, memID)
			}
			return apperror.Wrap(apperror.CodeInternalError, "soft delete memory", err)
		}

		deletedSnap := *current
		deletedSnap.Version = newVersion
		deletedSnap.UpdatedBy = access.principal.PrincipalID
		deletedSnap.UpdatedAt = now
		deletedSnap.DeletedAt = &now

		if err := insertRevision(ctx, tx, deletedSnap, "delete", access.principal.PrincipalID, reqID); err != nil {
			return err
		}

		evRev, err := s.appendEventLocked(ctx, tx, deletedSnap.TenantID, deletedSnap.ProjectID, deletedSnap.ID, "delete", deletedSnap.Version, access.principal.PrincipalID)
		if err != nil {
			return err
		}

		latestCur, err := s.signer.SignCursor(access.principal.TenantID, access.claims.ProjectID, evRev)
		if err != nil {
			return err
		}

		resp = DeleteResponse{
			MemoryID:     memID,
			Version:      newVersion,
			DeletedAt:    now,
			LatestCursor: latestCur,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Changes implements Section 7.7 memory_changes.
func (s *Service) Changes(ctx context.Context, req ChangesRequest) (*ChangesResponse, error) {
	access, err := s.verifyAccess(ctx, req.ProjectContext)
	if err != nil {
		return nil, err
	}

	var scopeProjID *uuid.UUID
	if access.claims.Mode == auth.ModeProject && access.claims.ProjectID != nil {
		if !access.perms.CanReadProject {
			return nil, apperror.New(apperror.CodeUnauthorized, "no permission to read project changes")
		}
		scopeProjID = access.claims.ProjectID
	}

	sinceRev, err := s.signer.VerifyCursor(strings.TrimSpace(req.Cursor), access.principal.TenantID, scopeProjID)
	if err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	// Read current tenant high-water mark before scanning events so advancing cursor is safe.
	highWaterRev, err := s.getTenantHighWaterRevision(ctx, access.principal.TenantID)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT revision, project_id, memory_id, operation, memory_version, actor_id, created_at
		FROM memory_events
		WHERE tenant_id = $1
		  AND revision > $2
		  AND (project_id IS NULL OR ($3::uuid IS NOT NULL AND project_id = $3))
		ORDER BY revision ASC
		LIMIT $4
	`, access.principal.TenantID, sinceRev, scopeProjID, limit+1)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "query memory_events", err)
	}
	defer rows.Close()

	events := make([]ChangeEvent, 0, limit)
	for rows.Next() {
		var ev ChangeEvent
		if err := rows.Scan(&ev.Revision, &ev.ProjectID, &ev.MemoryID, &ev.Operation, &ev.MemoryVersion, &ev.ActorID, &ev.CreatedAt); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan memory_event", err)
		}
		if ev.ProjectID == nil {
			ev.Scope = "global"
		} else {
			ev.Scope = "project"
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "iterate memory_events", err)
	}

	hasMore := false
	nextRev := sinceRev
	if len(events) > limit {
		hasMore = true
		events = events[:limit]
		nextRev = events[len(events)-1].Revision
	} else {
		// Section 8.6: when results < limit, advance cursor to tenant committed high-water mark
		if len(events) > 0 && events[len(events)-1].Revision > nextRev {
			nextRev = events[len(events)-1].Revision
		}
		if highWaterRev > nextRev {
			nextRev = highWaterRev
		}
	}

	nextCursor, err := s.signer.SignCursor(access.principal.TenantID, scopeProjID, nextRev)
	if err != nil {
		return nil, err
	}
	latestRev := highWaterRev
	if nextRev > latestRev {
		latestRev = nextRev
	}
	latestCursor, err := s.signer.SignCursor(access.principal.TenantID, scopeProjID, latestRev)
	if err != nil {
		return nil, err
	}

	return &ChangesResponse{
		Events:       events,
		NextCursor:   nextCursor,
		HasMore:      hasMore,
		LatestCursor: latestCursor,
	}, nil
}

// Promote implements Section 7.8 memory_promote.
func (s *Service) Promote(ctx context.Context, req PromoteRequest, reqID string) (*WriteMemoryResponse, error) {
	access, err := s.verifyAccess(ctx, req.ProjectContext)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimiter.Allow(access.principal.PrincipalID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 128 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "idempotency_key is required and must be <= 128 chars")
	}
	srcID, err := uuid.Parse(strings.TrimSpace(req.MemoryID))
	if err != nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id uuid")
	}
	if req.ExpectedVersion <= 0 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "expected_version must be > 0")
	}
	if req.Category != "" {
		if err := security.ValidateCategory(req.Category); err != nil {
			return nil, err
		}
	}
	if req.Title != "" {
		t := strings.TrimSpace(req.Title)
		if t == "" || len(t) > s.cfg.Limits.MaxTitleBytes {
			return nil, apperror.New(apperror.CodeInvalidArgument, "invalid title length")
		}
		if err := security.CheckTextSafety(t); err != nil {
			return nil, err
		}
	}
	if len(req.Tags) > 0 {
		if err := security.ValidateTags(s.cfg.Limits, req.Tags); err != nil {
			return nil, err
		}
	}

	reqHash := computeRequestHash("memory_promote", map[string]any{
		"memory_id":        srcID,
		"expected_version": req.ExpectedVersion,
		"category":         req.Category,
		"title":            req.Title,
		"tags":             req.Tags,
		"is_pinned":        req.IsPinned,
	})

	var resp WriteMemoryResponse
	err = s.runIdempotentWriteTx(ctx, access.principal, "memory_promote", req.IdempotencyKey, reqHash, &resp, func(tx pgx.Tx) error {
		srcMem, err := s.loadMemoryByID(ctx, tx, access.principal.TenantID, srcID)
		if err != nil {
			return err
		}
		if srcMem.DeletedAt != nil || !s.canReadMemory(access, srcMem) {
			return apperror.New(apperror.CodeMemoryNotFound, "memory not found")
		}
		if !access.perms.CanWriteGlobal {
			return apperror.New(apperror.CodeUnauthorized, "global_writer or admin role required to promote memory to global scope")
		}
		if srcMem.Scope != "project" {
			return apperror.New(apperror.CodeInvalidArgument, "only project memory can be promoted to global memory")
		}
		if srcMem.Version != req.ExpectedVersion {
			if s.metrics != nil {
				s.metrics.IncVersionConflict()
			}
			return apperror.New(apperror.CodeVersionConflict, "source memory version conflict").WithDetails(map[string]any{
				"memory_id":       srcMem.ID,
				"current_version": srcMem.Version,
			})
		}

		now := time.Now().UTC()
		newID := uuid.New()
		category := srcMem.Category
		if req.Category != "" {
			category = req.Category
		}
		title := srcMem.Title
		if req.Title != "" {
			title = strings.TrimSpace(req.Title)
		}
		tags := srcMem.Tags
		if len(req.Tags) > 0 {
			tags = normalizeTags(req.Tags)
		}
		isPinned := srcMem.IsPinned
		if req.IsPinned != nil {
			isPinned = *req.IsPinned
		}

		refsJSON, err := json.Marshal(srcMem.SourceRefs)
		if err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "marshal source_refs", err)
		}

		srcIDCopy := srcMem.ID
		promoted := MemoryDetail{
			ID:             newID,
			TenantID:       access.principal.TenantID,
			Scope:          "global",
			ProjectID:      nil,
			Category:       category,
			Title:          title,
			Content:        srcMem.Content,
			Tags:           tags,
			SourceRefs:     srcMem.SourceRefs,
			IsPinned:       isPinned,
			Status:         "active",
			Version:        1,
			SourceMemoryID: &srcIDCopy,
			ContentHash:    srcMem.ContentHash,
			CreatedBy:      access.principal.PrincipalID,
			UpdatedBy:      access.principal.PrincipalID,
			CreatedAt:      now,
			UpdatedAt:      now,
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO memories (
				id, tenant_id, scope, project_id, category, title, content_md,
				tags, source_refs, is_pinned, status, version, source_memory_id,
				content_hash, created_by, updated_by, created_at, updated_at
			) VALUES (
				$1, $2, 'global', NULL, $3, $4, $5,
				$6, $7, $8, 'active', 1, $9,
				$10, $11, $12, $13, $14
			)
		`,
			promoted.ID, promoted.TenantID, promoted.Category, promoted.Title, promoted.Content,
			promoted.Tags, refsJSON, promoted.IsPinned, promoted.SourceMemoryID,
			promoted.ContentHash, promoted.CreatedBy, promoted.UpdatedBy, promoted.CreatedAt, promoted.UpdatedAt,
		)
		if err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "insert promoted global memory", err)
		}

		// Section 7.8: only check duplicates within target Global scope; source Project Memory is not a duplicate
		dups, err := findDuplicateCandidates(ctx, tx, promoted.TenantID, "global", nil, promoted.ContentHash, promoted.ID)
		if err != nil {
			return err
		}

		if err := insertRevision(ctx, tx, promoted, "promote", access.principal.PrincipalID, reqID); err != nil {
			return err
		}

		evRev, err := s.appendEventLocked(ctx, tx, promoted.TenantID, nil, promoted.ID, "promote", promoted.Version, access.principal.PrincipalID)
		if err != nil {
			return err
		}

		latestCur, err := s.signer.SignCursor(access.principal.TenantID, access.claims.ProjectID, evRev)
		if err != nil {
			return err
		}

		resp = WriteMemoryResponse{
			Memory:              promoted,
			DuplicateCandidates: dups,
			LatestCursor:        latestCur,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Restore restores a soft-deleted memory (management operation for `memoryctl memory restore`, Section 7.6).
func (s *Service) Restore(ctx context.Context, req RestoreRequest) (*MemoryDetail, error) {
	if req.ExpectedVersion <= 0 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "expected_version must be > 0")
	}
	p, err := s.auth.VerifyPrincipalMembership(ctx, req.TenantID, req.ActorID)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "begin restore transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := s.loadMemoryByID(ctx, tx, req.TenantID, req.MemoryID)
	if err != nil {
		return nil, err
	}

	if current.Scope == "global" {
		if !auth.CanWriteGlobal(p.TenantRole) {
			return nil, apperror.New(apperror.CodeUnauthorized, "global_writer or admin role required to restore global memory")
		}
	} else {
		if current.ProjectID == nil {
			return nil, apperror.New(apperror.CodeMemoryNotFound, "memory not found")
		}
		projRole, err := s.auth.GetProjectRole(ctx, p.TenantID, *current.ProjectID, p.PrincipalID, p.TenantRole)
		if err != nil {
			return nil, err
		}
		if !auth.CanReadProject(projRole) {
			return nil, apperror.New(apperror.CodeMemoryNotFound, "memory not found")
		}
		if !auth.CanMaintainProject(projRole) {
			return nil, apperror.New(apperror.CodeUnauthorized, "maintainer role required to restore project memory")
		}
	}

	if current.DeletedAt == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "memory is not deleted")
	}
	if current.Version != req.ExpectedVersion {
		if s.metrics != nil {
			s.metrics.IncVersionConflict()
		}
		return nil, apperror.New(apperror.CodeVersionConflict, "memory version conflict").WithDetails(map[string]any{
			"memory_id":       current.ID,
			"current_version": current.Version,
		})
	}

	now := time.Now().UTC()
	var newVersion int64
	err = tx.QueryRow(ctx, `
		UPDATE memories
		SET deleted_at = NULL,
			version = version + 1,
			updated_by = $1,
			updated_at = $2
		WHERE id = $3
		  AND tenant_id = $4
		  AND version = $5
		  AND deleted_at IS NOT NULL
		RETURNING version
	`, p.PrincipalID, now, req.MemoryID, p.TenantID, req.ExpectedVersion).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperror.New(apperror.CodeVersionConflict, "memory version conflict")
		}
		return nil, apperror.Wrap(apperror.CodeInternalError, "restore memory", err)
	}

	restored := *current
	restored.DeletedAt = nil
	restored.Version = newVersion
	restored.UpdatedBy = p.PrincipalID
	restored.UpdatedAt = now

	reqID := req.RequestID
	if reqID == "" {
		reqID = "cli-restore-" + uuid.NewString()
	}
	if err := insertRevision(ctx, tx, restored, "restore", p.PrincipalID, reqID); err != nil {
		return nil, err
	}

	if _, err := s.appendEventLocked(ctx, tx, restored.TenantID, restored.ProjectID, restored.ID, "restore", restored.Version, p.PrincipalID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "commit restore transaction", err)
	}

	return &restored, nil
}

// loadMemoryForWriteCheck enforces the Section 9 error decision matrix:
// 1. Not found, deleted, or no read permission -> MEMORY_NOT_FOUND
// 2. Has read permission, but lacks write/maintain permission -> UNAUTHORIZED
// 3. Version mismatch -> handled either here or on conditional UPDATE
func (s *Service) loadMemoryForWriteCheck(ctx context.Context, q rowQuerier, access *verifiedAccess, memID uuid.UUID, isDelete bool) (*MemoryDetail, error) {
	mem, err := s.loadMemoryByID(ctx, q, access.principal.TenantID, memID)
	if err != nil {
		return nil, err
	}
	if mem.DeletedAt != nil || !s.canReadMemory(access, mem) {
		return nil, apperror.New(apperror.CodeMemoryNotFound, "memory not found")
	}

	if mem.Scope == "global" {
		if !access.perms.CanWriteGlobal {
			return nil, apperror.New(apperror.CodeUnauthorized, "global_writer or admin role required")
		}
	} else {
		if isDelete {
			if !access.perms.CanMaintainProject {
				return nil, apperror.New(apperror.CodeUnauthorized, "maintainer role required to delete project memory")
			}
		} else {
			if !access.perms.CanWriteProject {
				return nil, apperror.New(apperror.CodeUnauthorized, "writer or maintainer role required to update project memory")
			}
		}
	}
	return mem, nil
}

func (s *Service) conflictOrNotFoundError(ctx context.Context, q rowQuerier, access *verifiedAccess, memID uuid.UUID) error {
	mem, err := s.loadMemoryByID(ctx, q, access.principal.TenantID, memID)
	if err != nil {
		return err
	}
	if mem.DeletedAt != nil || !s.canReadMemory(access, mem) {
		return apperror.New(apperror.CodeMemoryNotFound, "memory not found")
	}
	if s.metrics != nil {
		s.metrics.IncVersionConflict()
	}
	return apperror.New(apperror.CodeVersionConflict, "expected_version does not match current version").WithDetails(map[string]any{
		"memory_id":       mem.ID,
		"current_version": mem.Version,
	})
}

func (s *Service) canReadMemory(access *verifiedAccess, mem *MemoryDetail) bool {
	if mem.TenantID != access.principal.TenantID {
		return false
	}
	if mem.Scope == "global" {
		return access.perms.CanReadGlobal
	}
	if mem.Scope == "project" && mem.ProjectID != nil {
		if access.claims.Mode != auth.ModeProject || access.claims.ProjectID == nil {
			return false
		}
		if *access.claims.ProjectID != *mem.ProjectID {
			return false
		}
		return access.perms.CanReadProject
	}
	return false
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Service) loadMemoryByID(ctx context.Context, q rowQuerier, tenantID, memID uuid.UUID) (*MemoryDetail, error) {
	var (
		m        MemoryDetail
		refsJSON []byte
	)
	err := q.QueryRow(ctx, `
		SELECT id, tenant_id, scope, project_id, category, title, content_md,
		       tags, source_refs, is_pinned, status, version, source_memory_id,
		       content_hash, created_by, updated_by, created_at, updated_at, deleted_at
		FROM memories
		WHERE id = $1 AND tenant_id = $2
	`, memID, tenantID).Scan(
		&m.ID, &m.TenantID, &m.Scope, &m.ProjectID, &m.Category, &m.Title, &m.Content,
		&m.Tags, &refsJSON, &m.IsPinned, &m.Status, &m.Version, &m.SourceMemoryID,
		&m.ContentHash, &m.CreatedBy, &m.UpdatedBy, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperror.New(apperror.CodeMemoryNotFound, "memory not found")
		}
		return nil, apperror.Wrap(apperror.CodeInternalError, "load memory by id", err)
	}
	if len(refsJSON) > 0 {
		if err := json.Unmarshal(refsJSON, &m.SourceRefs); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "unmarshal source_refs", err)
		}
	}
	if m.SourceRefs == nil {
		m.SourceRefs = []security.SourceRef{}
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return &m, nil
}

// appendEventLocked acquires the tenant-level transaction advisory lock (Section 8.6),
// inserts the memory_events row so revision order strictly matches commit order, and executes pg_notify.
func (s *Service) appendEventLocked(
	ctx context.Context,
	tx pgx.Tx,
	tenantID uuid.UUID,
	projectID *uuid.UUID,
	memoryID uuid.UUID,
	operation string,
	memoryVersion int64,
	actorID uuid.UUID,
) (int64, error) {
	start := time.Now()
	_, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended('memory-events:' || $1::text, 0)
		)
	`, tenantID.String())
	if err != nil {
		return 0, apperror.Wrap(apperror.CodeInternalError, "acquire tenant event advisory lock", err)
	}

	var revision int64
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO memory_events (
			tenant_id, project_id, memory_id, operation, memory_version, actor_id
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING revision, created_at
	`, tenantID, projectID, memoryID, operation, memoryVersion, actorID).Scan(&revision, &createdAt)
	if err != nil {
		return 0, apperror.Wrap(apperror.CodeInternalError, "insert memory_event", err)
	}

	payload, err := json.Marshal(notify.EventPayload{
		TenantID:      tenantID,
		ProjectID:     projectID,
		EventRevision: revision,
		CommittedAt:   createdAt,
	})
	if err == nil {
		_, _ = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, notify.ChannelName, string(payload))
	}

	if s.metrics != nil {
		s.metrics.ObserveEventLag(time.Since(start))
	}
	return revision, nil
}

// runIdempotentWriteTx executes fn inside a READ COMMITTED transaction guarded by idempotency_requests (Section 8.7 & 9).
func (s *Service) runIdempotentWriteTx(
	ctx context.Context,
	p auth.Principal,
	toolName string,
	idemKey string,
	reqHash string,
	outResp any,
	fn func(tx pgx.Tx) error,
) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "begin transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lockTimeoutMs := s.cfg.Database.LockTimeout.Milliseconds()
	if lockTimeoutMs <= 0 {
		lockTimeoutMs = 3000
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", lockTimeoutMs)); err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "set lock_timeout", err)
	}

	// Clean up any expired idempotency row for this exact key
	_, err = tx.Exec(ctx, `
		DELETE FROM idempotency_requests
		WHERE tenant_id = $1
		  AND principal_id = $2
		  AND tool_name = $3
		  AND idempotency_key = $4
		  AND expires_at <= now()
	`, p.TenantID, p.PrincipalID, toolName, idemKey)
	if err != nil {
		if isLockTimeoutError(err) {
			return apperror.New(apperror.CodeIdempotencyInProgress, "another request with the same idempotency_key is currently in progress")
		}
		return apperror.Wrap(apperror.CodeInternalError, "cleanup expired idempotency key", err)
	}

	idemID := uuid.New()
	expiresAt := time.Now().UTC().Add(s.cfg.Limits.IdempotencyTTL)
	var insertedID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO idempotency_requests (
			id, tenant_id, principal_id, tool_name, idempotency_key, request_hash, status, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'in_progress', $7)
		ON CONFLICT (tenant_id, principal_id, tool_name, idempotency_key) DO NOTHING
		RETURNING id
	`, idemID, p.TenantID, p.PrincipalID, toolName, idemKey, reqHash, expiresAt).Scan(&insertedID)

	if err != nil {
		if isLockTimeoutError(err) {
			return apperror.New(apperror.CodeIdempotencyInProgress, "another request with the same idempotency_key is currently in progress")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return apperror.Wrap(apperror.CodeInternalError, "insert idempotency_request", err)
		}

		// Conflict hit: another transaction already committed a record with this key
		var existingHash, existingStatus string
		var respBytes []byte
		qErr := tx.QueryRow(ctx, `
			SELECT request_hash, status, response_json
			FROM idempotency_requests
			WHERE tenant_id = $1
			  AND principal_id = $2
			  AND tool_name = $3
			  AND idempotency_key = $4
		`, p.TenantID, p.PrincipalID, toolName, idemKey).Scan(&existingHash, &existingStatus, &respBytes)
		if qErr != nil {
			return apperror.Wrap(apperror.CodeInternalError, "load existing idempotency_request", qErr)
		}
		if existingHash != reqHash {
			return apperror.New(apperror.CodeIdempotencyConflict, "idempotency_key was already used with different request parameters")
		}
		if existingStatus != "completed" || len(respBytes) == 0 {
			s.log.Error().
				Str("tenant_id", p.TenantID.String()).
				Str("tool", toolName).
				Str("idempotency_key", idemKey).
				Msg("detected abnormally committed in_progress idempotency record")
			return apperror.New(apperror.CodeIdempotencyInProgress, "idempotent request is in an incomplete state")
		}
		if s.metrics != nil {
			s.metrics.IncIdempotencyHit()
		}
		if err := json.Unmarshal(respBytes, outResp); err != nil {
			return apperror.Wrap(apperror.CodeInternalError, "unmarshal cached idempotent response", err)
		}
		return nil
	}

	if err := fn(tx); err != nil {
		return err
	}

	respBytes, err := json.Marshal(outResp)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "marshal idempotent response", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE idempotency_requests
		SET status = 'completed',
			response_json = $1
		WHERE id = $2
	`, respBytes, insertedID)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "complete idempotency_request", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "commit write transaction", err)
	}
	return nil
}

func insertRevision(ctx context.Context, tx pgx.Tx, detail MemoryDetail, operation string, actorID uuid.UUID, reqID string) error {
	if reqID == "" {
		reqID = uuid.NewString()
	}
	snapBytes, err := json.Marshal(detail)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "marshal revision snapshot", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO memory_revisions (
			tenant_id, memory_id, version, operation, snapshot, actor_id, request_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, detail.TenantID, detail.ID, detail.Version, operation, snapBytes, actorID, reqID, detail.UpdatedAt)
	if err != nil {
		return apperror.Wrap(apperror.CodeInternalError, "insert memory_revision", err)
	}
	return nil
}

func findDuplicateCandidates(
	ctx context.Context,
	tx pgx.Tx,
	tenantID uuid.UUID,
	scope string,
	projectID *uuid.UUID,
	contentHash string,
	excludeID uuid.UUID,
) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT id
		FROM memories
		WHERE tenant_id = $1
		  AND scope = $2
		  AND ($3::uuid IS NULL OR project_id = $3)
		  AND content_hash = $4
		  AND id <> $5
		  AND deleted_at IS NULL
		ORDER BY updated_at DESC
		LIMIT 5
	`, tenantID, scope, projectID, contentHash, excludeID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "query duplicate candidates", err)
	}
	defer rows.Close()

	var dups []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "scan duplicate candidate", err)
		}
		dups = append(dups, id)
	}
	return dups, rows.Err()
}

func (s *Service) getTenantHighWaterRevision(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var rev int64
	err := s.pool.QueryRow(ctx, `
		SELECT coalesce(max(revision), 0)
		FROM memory_events
		WHERE tenant_id = $1
	`, tenantID).Scan(&rev)
	if err != nil {
		return 0, apperror.Wrap(apperror.CodeInternalError, "query high-water revision", err)
	}
	return rev, nil
}

func (s *Service) getLatestCursor(ctx context.Context, tenantID uuid.UUID, projectID *uuid.UUID) (string, error) {
	rev, err := s.getTenantHighWaterRevision(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return s.signer.SignCursor(tenantID, projectID, rev)
}

func scanIndexItem(rows pgx.Rows) (IndexItem, error) {
	var item IndexItem
	err := rows.Scan(
		&item.ID,
		&item.Category,
		&item.Title,
		&item.Tags,
		&item.UpdatedAt,
	)
	if err != nil {
		return item, apperror.Wrap(apperror.CodeInternalError, "scan index item", err)
	}
	return item, nil
}

func scanMemorySummary(rows pgx.Rows, query string) (MemorySummary, error) {
	var (
		sum     MemorySummary
		content string
	)
	err := rows.Scan(
		&sum.ID,
		&sum.Scope,
		&sum.Category,
		&sum.Title,
		&content,
		&sum.Tags,
		&sum.IsPinned,
		&sum.Status,
		&sum.Version,
		&sum.UpdatedAt,
	)
	if err != nil {
		return sum, apperror.Wrap(apperror.CodeInternalError, "scan memory summary", err)
	}
	sum.Summary = summarizeContent(content, query, 220)
	return sum, nil
}

func summarizeContent(md, query string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 220
	}
	body, desc := stripYAMLFrontmatter(md)
	body = compactBlankLines(body)

	q := strings.TrimSpace(query)
	if q != "" {
		if body != "" {
			if start, end, ok := findBestQueryMatch([]rune(body), q, maxRunes); ok {
				return sliceRuneWindow([]rune(body), start, end, maxRunes)
			}
		}
		if desc != "" {
			if start, end, ok := findBestQueryMatch([]rune(desc), q, maxRunes); ok {
				return sliceRuneWindow([]rune(desc), start, end, maxRunes)
			}
		}
	}

	var combined string
	switch {
	case desc != "" && body != "":
		if strings.Contains(strings.ToLower(body), strings.ToLower(desc)) {
			combined = body
		} else {
			combined = desc + "\n" + body
		}
	case body != "":
		combined = body
	case desc != "":
		combined = desc
	default:
		combined = compactBlankLines(md)
	}

	runes := []rune(combined)
	if len(runes) <= maxRunes {
		return combined
	}
	return string(runes[:maxRunes]) + "..."
}

func stripYAMLFrontmatter(md string) (body, description string) {
	normalized := strings.ReplaceAll(strings.TrimPrefix(md, "\ufeff"), "\r\n", "\n")
	trimmed := strings.TrimLeft(normalized, " \t\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return strings.TrimSpace(normalized), ""
	}

	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" || t == "..." {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return strings.TrimSpace(normalized), ""
	}

	fmLines := lines[1:closeIdx]
	for i := 0; i < len(fmLines); i++ {
		line := fmLines[i]
		trimmedLine := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmedLine, "description:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "description:"))
		if val == "|" || val == ">" || val == "|-" || val == ">-" {
			var multi []string
			for j := i + 1; j < len(fmLines); j++ {
				if fmLines[j] == "" {
					if len(multi) > 0 {
						multi = append(multi, "")
					}
					continue
				}
				if !strings.HasPrefix(fmLines[j], " ") && !strings.HasPrefix(fmLines[j], "\t") {
					break
				}
				multi = append(multi, strings.TrimSpace(fmLines[j]))
			}
			description = strings.TrimSpace(strings.Join(multi, " "))
		} else {
			description = unquoteYAMLValue(val)
		}
		break
	}

	body = strings.TrimSpace(strings.Join(lines[closeIdx+1:], "\n"))
	return body, strings.TrimSpace(description)
}

func unquoteYAMLValue(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return strings.TrimSpace(v[1 : len(v)-1])
		}
	}
	return strings.TrimSpace(v)
}

func compactBlankLines(s string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	if normalized == "" {
		return ""
	}
	lines := strings.Split(normalized, "\n")
	out := make([]string, 0, len(lines))
	prevEmpty := false
	for _, line := range lines {
		t := strings.TrimRight(line, " \t")
		if strings.TrimSpace(t) == "" {
			if !prevEmpty && len(out) > 0 {
				out = append(out, "")
			}
			prevEmpty = true
			continue
		}
		prevEmpty = false
		out = append(out, t)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func findBestQueryMatch(haystack []rune, query string, windowRunes int) (start, end int, found bool) {
	if len(haystack) == 0 {
		return 0, 0, false
	}
	fullNeedle := []rune(strings.TrimSpace(query))
	if len(fullNeedle) > 0 {
		if idx := indexRunesFold(haystack, fullNeedle, 0); idx >= 0 {
			return idx, idx + len(fullNeedle), true
		}
	}

	fields := strings.Fields(query)
	if len(fields) == 0 {
		return 0, 0, false
	}

	type hit struct {
		tokenIdx int
		start    int
		end      int
	}
	var hits []hit
	for ti, f := range fields {
		needle := []rune(f)
		if len(needle) == 0 {
			continue
		}
		pos := 0
		for pos < len(haystack) {
			idx := indexRunesFold(haystack, needle, pos)
			if idx < 0 {
				break
			}
			hits = append(hits, hit{tokenIdx: ti, start: idx, end: idx + len(needle)})
			pos = idx + len(needle)
		}
	}
	if len(hits) == 0 {
		return 0, 0, false
	}

	bestScore := -1
	bestHit := hits[0]
	halfWin := windowRunes / 2
	for _, anchor := range hits {
		winLo := anchor.start - halfWin
		winHi := anchor.start + halfWin
		seenTokens := make(map[int]bool, len(fields))
		for _, other := range hits {
			if other.start >= winLo && other.start <= winHi {
				seenTokens[other.tokenIdx] = true
			}
		}
		score := len(seenTokens)
		if score > bestScore || (score == bestScore && anchor.start < bestHit.start) {
			bestScore = score
			bestHit = anchor
		}
	}
	return bestHit.start, bestHit.end, true
}

func indexRunesFold(haystack, needle []rune, from int) int {
	nLen := len(needle)
	if nLen == 0 || from < 0 || from+nLen > len(haystack) {
		return -1
	}
	limit := len(haystack) - nLen
	for i := from; i <= limit; i++ {
		matched := true
		for j := 0; j < nLen; j++ {
			if unicode.ToLower(haystack[i+j]) != unicode.ToLower(needle[j]) {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func sliceRuneWindow(runes []rune, matchStart, matchEnd, maxRunes int) string {
	if len(runes) <= maxRunes {
		return string(runes)
	}
	// Reserve ~30% before the match and ~70% from matchStart onward so values after a matched label stay visible.
	preContext := maxRunes / 3
	winStart := matchStart - preContext
	if winStart < 0 {
		winStart = 0
	}
	winEnd := winStart + maxRunes
	if winEnd > len(runes) {
		winEnd = len(runes)
		winStart = winEnd - maxRunes
		if winStart < 0 {
			winStart = 0
		}
	}
	if matchEnd > winEnd && matchEnd-matchStart <= maxRunes {
		winEnd = matchEnd
		winStart = winEnd - maxRunes
		if winStart < 0 {
			winStart = 0
		}
	}

	var b strings.Builder
	if winStart > 0 {
		b.WriteString("...")
	}
	b.WriteString(strings.TrimSpace(string(runes[winStart:winEnd])))
	if winEnd < len(runes) {
		b.WriteString("...")
	}
	return b.String()
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func isPatchEmpty(p MemoryPatch) bool {
	return p.Category == nil &&
		p.Title == nil &&
		p.Content == nil &&
		p.Tags == nil &&
		p.SourceRefs == nil &&
		p.Status == nil &&
		p.IsPinned == nil
}

func computeRequestHash(tool string, payload any) string {
	b, _ := json.Marshal(map[string]any{
		"tool":    tool,
		"payload": payload,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func isLockTimeoutError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// 55P03 = lock_not_available, 57014 = query_canceled
		return pgErr.Code == "55P03" || pgErr.Code == "57014"
	}
	return false
}

type queryCursor struct {
	Offset    int        `json:"o"`
	IsPinned  *bool      `json:"p,omitempty"`
	UpdatedAt *time.Time `json:"u,omitempty"`
	ID        *uuid.UUID `json:"i,omitempty"`
}

func encodeQueryCursor(c queryCursor) string {
	raw, _ := json.Marshal(c)
	return "qcur_" + base64.RawURLEncoding.EncodeToString(raw)
}

func decodeQueryCursor(s string) (queryCursor, error) {
	if strings.HasPrefix(s, "cur_") {
		return queryCursor{}, apperror.New(apperror.CodeInvalidArgument, "cursor with 'cur_' prefix is a change-feed cursor for memory_changes; pass next_cursor ('qcur_...') for memory_query pagination")
	}
	if strings.HasPrefix(s, "qcur_") {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "qcur_"))
		if err != nil {
			return queryCursor{}, apperror.New(apperror.CodeInvalidArgument, "invalid query pagination cursor encoding")
		}
		var c queryCursor
		if err := json.Unmarshal(raw, &c); err != nil || c.Offset < 0 {
			return queryCursor{}, apperror.New(apperror.CodeInvalidArgument, "malformed query pagination cursor")
		}
		return c, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return queryCursor{}, apperror.New(apperror.CodeInvalidArgument, "invalid query pagination cursor")
	}
	return queryCursor{Offset: n}, nil
}
