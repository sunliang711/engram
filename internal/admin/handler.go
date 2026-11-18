package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"engram/internal/apperror"
	"engram/internal/auth"
	"engram/internal/exporter"
	"engram/internal/memory"
	"engram/internal/project"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

//go:embed ui/*
var uiFS embed.FS

// Handler serves the embedded Admin Web UI and all /api/admin/* management endpoints.
type Handler struct {
	pool         *pgxpool.Pool
	authMgr      *AuthManager
	memSvc       *memory.Service
	exporter     *exporter.Exporter
	loginLimiter *auth.RateLimiter
	log          zerolog.Logger
}

func NewHandler(
	pool *pgxpool.Pool,
	authMgr *AuthManager,
	memSvc *memory.Service,
	exp *exporter.Exporter,
	log zerolog.Logger,
) *Handler {
	return &Handler{
		pool:         pool,
		authMgr:      authMgr,
		memSvc:       memSvc,
		exporter:     exp,
		loginLimiter: auth.NewRateLimiter(20, 8),
		log:          log,
	}
}

// RegisterRoutes attaches the Web UI and /api/admin/* routes to mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	subFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic("failed to mount embedded ui fs: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(subFS))

	mux.Handle("/ui/", http.StripPrefix("/ui/", fileServer))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		indexBytes, _ := uiFS.ReadFile("ui/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	})

	// Public / Session Admin Auth APIs
	mux.HandleFunc("/api/admin/session", h.withSafety(h.handleSession))
	mux.HandleFunc("/api/admin/login", h.withSafety(h.handleLogin))
	mux.HandleFunc("/api/admin/logout", h.withSafety(h.handleLogout))
	mux.HandleFunc("/api/admin/change-password", h.withSafety(h.handleChangePassword))

	// Protected Management APIs (require authenticated session with must_change_password == false)
	mux.HandleFunc("/api/admin/overview", h.requireActiveAdmin(h.handleOverview))
	mux.HandleFunc("/api/admin/quickstart", h.requireActiveAdmin(h.handleQuickstart))
	mux.HandleFunc("/api/admin/tenants", h.requireActiveAdmin(h.handleTenants))
	mux.HandleFunc("/api/admin/principals", h.requireActiveAdmin(h.handlePrincipals))
	mux.HandleFunc("/api/admin/projects", h.requireActiveAdmin(h.handleProjects))
	mux.HandleFunc("/api/admin/projects/members", h.requireActiveAdmin(h.handleProjectMembers))
	mux.HandleFunc("/api/admin/projects/remotes", h.requireActiveAdmin(h.handleProjectRemotes))
	mux.HandleFunc("/api/admin/tokens", h.requireActiveAdmin(h.handleTokens))
	mux.HandleFunc("/api/admin/tokens/reveal", h.requireActiveAdmin(h.handleRevealToken))
	mux.HandleFunc("/api/admin/tokens/revoke", h.requireActiveAdmin(h.handleRevokeToken))
	mux.HandleFunc("/api/admin/memories", h.requireActiveAdmin(h.handleMemories))
	mux.HandleFunc("/api/admin/memories/revisions", h.requireActiveAdmin(h.handleMemoryRevisions))
	mux.HandleFunc("/api/admin/memories/restore", h.requireActiveAdmin(h.handleRestoreMemory))
	mux.HandleFunc("/api/admin/export", h.requireActiveAdmin(h.handleExport))
	mux.HandleFunc("/api/admin/export/archive", h.requireActiveAdmin(h.handleExportArchive))
}

func (h *Handler) withSafety(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

			// Same-Origin / CSRF check
			if secSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); secSite == "cross-site" {
				writeJSONError(w, apperror.New(apperror.CodeUnauthorized, "cross-site requests are forbidden"))
				return
			}
			if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && r.Host != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeJSONError(w, apperror.New(apperror.CodeUnauthorized, "origin mismatch"))
					return
				}
			}
			if r.ContentLength > 0 {
				ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
				if !strings.HasPrefix(ct, "application/json") {
					writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "Content-Type must be application/json"))
					return
				}
			}
		}
		next(w, r)
	}
}

func (h *Handler) extractSessionToken(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value)
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return ""
}

func (h *Handler) isSecureRequest(r *http.Request) bool {
	if h.authMgr.CookieSecure() || r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (h *Handler) requireActiveAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.withSafety(func(w http.ResponseWriter, r *http.Request) {
		token := h.extractSessionToken(r)
		state, err := h.authMgr.VerifySession(r.Context(), token)
		if err != nil || !state.Authenticated {
			writeJSONError(w, apperror.New(apperror.CodeUnauthenticated, "admin authentication required"))
			return
		}
		if state.MustChangePassword {
			writeJSONError(w, apperror.New(apperror.CodeUnauthorized, "admin password change required before accessing management console"))
			return
		}
		next(w, r)
	})
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := h.extractSessionToken(r)
	state, err := h.authMgr.VerifySession(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusOK, AdminState{
			Authenticated:       false,
			ResetAdminEnvActive: state.ResetAdminEnvActive,
		})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	clientIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		clientIP = host
	}
	if err := h.loginLimiter.AllowKey("login:" + clientIP); err != nil {
		writeJSONError(w, err)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	token, state, err := h.authMgr.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	h.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "logged_out"})
}

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := h.extractSessionToken(r)
	state, err := h.authMgr.VerifySession(r.Context(), token)
	if err != nil || !state.Authenticated {
		writeJSONError(w, apperror.New(apperror.CodeUnauthenticated, "admin authentication required"))
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}

	newToken, newState, err := h.authMgr.ChangePassword(r.Context(), state.Username, req.CurrentPassword, req.NewPassword)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	h.setSessionCookie(w, r, newToken)
	writeJSON(w, http.StatusOK, newState)
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var (
		tenantsCnt, principalsCnt, projectsCnt      int
		activeMemCnt, deletedMemCnt, activeTokenCnt int
		eventsCnt                                   int64
	)
	err := h.pool.QueryRow(r.Context(), `
		SELECT
			(SELECT count(*)::int FROM tenants),
			(SELECT count(*)::int FROM principals),
			(SELECT count(*)::int FROM projects),
			(SELECT count(*)::int FROM memories WHERE deleted_at IS NULL),
			(SELECT count(*)::int FROM memories WHERE deleted_at IS NOT NULL),
			(SELECT count(*)::int FROM api_tokens WHERE revoked_at IS NULL),
			(SELECT count(*)::bigint FROM memory_events)
	`).Scan(&tenantsCnt, &principalsCnt, &projectsCnt, &activeMemCnt, &deletedMemCnt, &activeTokenCnt, &eventsCnt)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "query overview stats", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenants_count":          tenantsCnt,
		"principals_count":       principalsCnt,
		"projects_count":         projectsCnt,
		"active_memories_count":  activeMemCnt,
		"deleted_memories_count": deletedMemCnt,
		"active_tokens_count":    activeTokenCnt,
		"events_count":           eventsCnt,
	})
}

func (h *Handler) handleQuickstart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TenantName           string `json:"tenant_name"`
		PrincipalSubject     string `json:"principal_subject"`
		PrincipalDisplayName string `json:"principal_display_name"`
		TenantRole           string `json:"tenant_role"`
		ProjectName          string `json:"project_name"`
		RemoteURL            string `json:"remote_url"`
		WorkspacePath        string `json:"workspace_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}

	req.TenantName = strings.TrimSpace(req.TenantName)
	req.PrincipalSubject = strings.TrimSpace(req.PrincipalSubject)
	req.PrincipalDisplayName = strings.TrimSpace(req.PrincipalDisplayName)
	req.TenantRole = strings.TrimSpace(req.TenantRole)
	if req.TenantRole == "" {
		req.TenantRole = "global_writer"
	}
	if req.TenantName == "" || req.PrincipalSubject == "" || req.PrincipalDisplayName == "" {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "tenant_name, principal_subject, and principal_display_name are required"))
		return
	}

	var canonRemote, wsPath string
	var err error
	if strings.TrimSpace(req.RemoteURL) != "" {
		canonRemote, err = project.NormalizeRemote(req.RemoteURL)
		if err != nil {
			writeJSONError(w, err)
			return
		}
	}
	if strings.TrimSpace(req.WorkspacePath) != "" {
		wsPath, err = project.NormalizeRelPath(req.WorkspacePath)
		if err != nil {
			writeJSONError(w, err)
			return
		}
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin quickstart tx", err))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Find or create tenant
	var tenantID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM tenants WHERE name = $1 LIMIT 1`, req.TenantName).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		tenantID = uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, status) VALUES ($1, $2, 'active')`, tenantID, req.TenantName); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert tenant", err))
			return
		}
	} else if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "query tenant", err))
		return
	}

	// 2. Upsert principal
	var principalID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO principals (id, identity_provider, subject, display_name, status)
		VALUES ($1, 'api_token', $2, $3, 'active')
		ON CONFLICT (identity_provider, subject) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING id
	`, uuid.New(), req.PrincipalSubject, req.PrincipalDisplayName).Scan(&principalID)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "upsert principal", err))
		return
	}

	// 3. Upsert tenant_member
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_members (tenant_id, principal_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, principal_id) DO UPDATE SET role = EXCLUDED.role
	`, tenantID, principalID, req.TenantRole); err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "assign tenant role", err))
		return
	}

	// 4. Optional project + repository + project_member
	var projectID *uuid.UUID
	var markerUUID *uuid.UUID
	if strings.TrimSpace(req.ProjectName) != "" {
		pid := uuid.New()
		m := uuid.New()
		projectID = &pid
		markerUUID = &m
		if _, err := tx.Exec(ctx, `
			INSERT INTO projects (id, tenant_id, name, marker, status)
			VALUES ($1, $2, $3, $4, 'active')
		`, pid, tenantID, strings.TrimSpace(req.ProjectName), m); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert project", err))
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO project_members (project_id, principal_id, role)
			VALUES ($1, $2, 'maintainer')
		`, pid, principalID); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert project_member", err))
			return
		}
		if canonRemote != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary)
				VALUES ($1, $2, $3, $4, $5, true)
				ON CONFLICT (tenant_id, canonical_remote, workspace_path) DO NOTHING
			`, uuid.New(), tenantID, pid, canonRemote, wsPath); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert project_repository", err))
				return
			}
		}
	}

	// 5. Issue API token
	rawToken, prefix, tokenHash, err := generateAPIToken()
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "generate api token", err))
		return
	}
	sealedToken, err := h.authMgr.SealToken(rawToken)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "seal api token", err))
		return
	}
	tokenID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO api_tokens (id, token_hash, token_prefix, token_ciphertext, tenant_id, principal_id, name)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, tokenID, tokenHash, prefix, sealedToken, tenantID, principalID, "quickstart-"+req.PrincipalSubject); err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert api token", err))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit quickstart tx", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":        tenantID,
		"principal_id":     principalID,
		"project_id":       projectID,
		"project_marker":   markerUUID,
		"canonical_remote": canonRemote,
		"token_id":         tokenID,
		"token":            rawToken,
	})
}

func (h *Handler) handleTenants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		rows, err := h.pool.Query(ctx, `
			SELECT id, name, status, created_at
			FROM tenants
			ORDER BY created_at DESC
		`)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list tenants", err))
			return
		}
		defer rows.Close()

		items := make([]map[string]any, 0)
		for rows.Next() {
			var id uuid.UUID
			var name, status string
			var createdAt time.Time
			if err := rows.Scan(&id, &name, &status, &createdAt); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "scan tenant", err))
				return
			}
			items = append(items, map[string]any{
				"id":         id,
				"name":       name,
				"status":     status,
				"created_at": createdAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})

	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "tenant name is required"))
			return
		}
		id := uuid.New()
		_, err := h.pool.Exec(ctx, `
			INSERT INTO tenants (id, name, status) VALUES ($1, $2, 'active')
		`, id, strings.TrimSpace(req.Name))
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "create tenant", err))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": strings.TrimSpace(req.Name), "status": "active"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handlePrincipals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		rows, err := h.pool.Query(ctx, `
			SELECT id, identity_provider, subject, display_name, status, created_at
			FROM principals
			ORDER BY created_at DESC
		`)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list principals", err))
			return
		}
		defer rows.Close()

		type prinItem struct {
			ID               uuid.UUID        `json:"id"`
			IdentityProvider string           `json:"identity_provider"`
			Subject          string           `json:"subject"`
			DisplayName      string           `json:"display_name"`
			Status           string           `json:"status"`
			CreatedAt        time.Time        `json:"created_at"`
			TenantRoles      []map[string]any `json:"tenant_roles"`
		}
		list := make([]*prinItem, 0)
		byID := make(map[uuid.UUID]*prinItem)

		for rows.Next() {
			var p prinItem
			if err := rows.Scan(&p.ID, &p.IdentityProvider, &p.Subject, &p.DisplayName, &p.Status, &p.CreatedAt); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "scan principal", err))
				return
			}
			p.TenantRoles = make([]map[string]any, 0)
			list = append(list, &p)
			byID[p.ID] = &p
		}
		rows.Close()

		tmRows, err := h.pool.Query(ctx, `
			SELECT tm.principal_id, tm.tenant_id, t.name, tm.role
			FROM tenant_members tm
			JOIN tenants t ON t.id = tm.tenant_id
		`)
		if err == nil {
			defer tmRows.Close()
			for tmRows.Next() {
				var pid, tid uuid.UUID
				var tName, role string
				if err := tmRows.Scan(&pid, &tid, &tName, &role); err == nil {
					if p, ok := byID[pid]; ok {
						p.TenantRoles = append(p.TenantRoles, map[string]any{
							"tenant_id":   tid,
							"tenant_name": tName,
							"role":        role,
						})
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": list})

	case http.MethodPost:
		var req struct {
			TenantID         string `json:"tenant_id"`
			IdentityProvider string `json:"identity_provider"`
			Subject          string `json:"subject"`
			DisplayName      string `json:"display_name"`
			TenantRole       string `json:"tenant_role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
		tid, err := uuid.Parse(req.TenantID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
			return
		}
		if req.IdentityProvider == "" {
			req.IdentityProvider = "api_token"
		}
		if strings.TrimSpace(req.Subject) == "" || strings.TrimSpace(req.DisplayName) == "" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "subject and display_name are required"))
			return
		}
		if req.TenantRole == "" {
			req.TenantRole = "member"
		}

		tx, err := h.pool.Begin(ctx)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin tx", err))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var pid uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO principals (id, identity_provider, subject, display_name, status)
			VALUES ($1, $2, $3, $4, 'active')
			ON CONFLICT (identity_provider, subject) DO UPDATE SET display_name = EXCLUDED.display_name
			RETURNING id
		`, uuid.New(), strings.TrimSpace(req.IdentityProvider), strings.TrimSpace(req.Subject), strings.TrimSpace(req.DisplayName)).Scan(&pid)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "upsert principal", err))
			return
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO tenant_members (tenant_id, principal_id, role)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, principal_id) DO UPDATE SET role = EXCLUDED.role
		`, tid, pid, req.TenantRole)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "bind tenant role", err))
			return
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit principal tx", err))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"principal_id": pid, "tenant_id": tid, "role": req.TenantRole})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		rows, err := h.pool.Query(ctx, `
			SELECT p.id, p.tenant_id, t.name, p.name, p.marker, p.status, p.created_at
			FROM projects p
			JOIN tenants t ON t.id = p.tenant_id
			ORDER BY p.created_at DESC
		`)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list projects", err))
			return
		}
		defer rows.Close()

		type projItem struct {
			ID           uuid.UUID        `json:"id"`
			TenantID     uuid.UUID        `json:"tenant_id"`
			TenantName   string           `json:"tenant_name"`
			Name         string           `json:"name"`
			Marker       *uuid.UUID       `json:"marker,omitempty"`
			Status       string           `json:"status"`
			CreatedAt    time.Time        `json:"created_at"`
			Repositories []map[string]any `json:"repositories"`
			Members      []map[string]any `json:"members"`
		}
		list := make([]*projItem, 0)
		byID := make(map[uuid.UUID]*projItem)
		for rows.Next() {
			var p projItem
			if err := rows.Scan(&p.ID, &p.TenantID, &p.TenantName, &p.Name, &p.Marker, &p.Status, &p.CreatedAt); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "scan project", err))
				return
			}
			p.Repositories = make([]map[string]any, 0)
			p.Members = make([]map[string]any, 0)
			list = append(list, &p)
			byID[p.ID] = &p
		}
		rows.Close()

		repoRows, err := h.pool.Query(ctx, `
			SELECT id, project_id, canonical_remote, workspace_path, is_primary
			FROM project_repositories
			ORDER BY is_primary DESC, created_at ASC
		`)
		if err == nil {
			defer repoRows.Close()
			for repoRows.Next() {
				var rid, pid uuid.UUID
				var canon, ws string
				var primary bool
				if err := repoRows.Scan(&rid, &pid, &canon, &ws, &primary); err == nil {
					if p, ok := byID[pid]; ok {
						p.Repositories = append(p.Repositories, map[string]any{
							"id":               rid,
							"canonical_remote": canon,
							"workspace_path":   ws,
							"is_primary":       primary,
						})
					}
				}
			}
		}

		memRows, err := h.pool.Query(ctx, `
			SELECT pm.project_id, pm.principal_id, pr.display_name, pm.role
			FROM project_members pm
			JOIN principals pr ON pr.id = pm.principal_id
		`)
		if err == nil {
			defer memRows.Close()
			for memRows.Next() {
				var pid, prinID uuid.UUID
				var dName, role string
				if err := memRows.Scan(&pid, &prinID, &dName, &role); err == nil {
					if p, ok := byID[pid]; ok {
						p.Members = append(p.Members, map[string]any{
							"principal_id": prinID,
							"display_name": dName,
							"role":         role,
						})
					}
				}
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{"items": list})

	case http.MethodPost:
		var req struct {
			TenantID      string `json:"tenant_id"`
			Name          string `json:"name"`
			RemoteURL     string `json:"remote_url"`
			WorkspacePath string `json:"workspace_path"`
			WithMarker    bool   `json:"with_marker"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
		tid, err := uuid.Parse(req.TenantID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "project name is required"))
			return
		}
		wsPath, err := project.NormalizeRelPath(req.WorkspacePath)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		var canonRemote string
		if strings.TrimSpace(req.RemoteURL) != "" {
			canonRemote, err = project.NormalizeRemote(req.RemoteURL)
			if err != nil {
				writeJSONError(w, err)
				return
			}
		}

		var marker *uuid.UUID
		if req.WithMarker || canonRemote == "" {
			m := uuid.New()
			marker = &m
		}

		tx, err := h.pool.Begin(ctx)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin tx", err))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		projID := uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO projects (id, tenant_id, name, marker, status)
			VALUES ($1, $2, $3, $4, 'active')
		`, projID, tid, strings.TrimSpace(req.Name), marker)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert project", err))
			return
		}

		if canonRemote != "" {
			_, err = tx.Exec(ctx, `
				INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary)
				VALUES ($1, $2, $3, $4, $5, true)
			`, uuid.New(), tid, projID, canonRemote, wsPath)
			if err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "insert project_repository", err))
				return
			}
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit project registration", err))
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"project_id":       projID,
			"tenant_id":        tid,
			"name":             strings.TrimSpace(req.Name),
			"marker":           marker,
			"canonical_remote": canonRemote,
			"workspace_path":   wsPath,
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleProjectMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ProjectID   string `json:"project_id"`
		PrincipalID string `json:"principal_id"`
		Role        string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	pid, err := uuid.Parse(req.ProjectID)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid project_id"))
		return
	}
	prinID, err := uuid.Parse(req.PrincipalID)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid principal_id"))
		return
	}

	// Verify that principal belongs to the same tenant as the target project
	var belongsToTenant bool
	err = h.pool.QueryRow(r.Context(), `
		SELECT EXISTS (
			SELECT 1
			FROM projects p
			JOIN tenant_members tm ON tm.tenant_id = p.tenant_id
			WHERE p.id = $1 AND tm.principal_id = $2
		)
	`, pid, prinID).Scan(&belongsToTenant)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "check tenant membership for project member", err))
		return
	}
	if !belongsToTenant {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "principal must be a member of the project's tenant first"))
		return
	}

	_, err = h.pool.Exec(r.Context(), `
		INSERT INTO project_members (project_id, principal_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id, principal_id) DO UPDATE SET role = EXCLUDED.role
	`, pid, prinID, req.Role)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "upsert project member", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": pid, "principal_id": prinID, "role": req.Role})
}

func (h *Handler) handleProjectRemotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ProjectID     string `json:"project_id"`
		RemoteURL     string `json:"remote_url"`
		WorkspacePath string `json:"workspace_path"`
		IsPrimary     bool   `json:"is_primary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	pid, err := uuid.Parse(req.ProjectID)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid project_id"))
		return
	}
	canonRemote, err := project.NormalizeRemote(req.RemoteURL)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	wsPath, err := project.NormalizeRelPath(req.WorkspacePath)
	if err != nil {
		writeJSONError(w, err)
		return
	}

	var tid uuid.UUID
	err = h.pool.QueryRow(r.Context(), `SELECT tenant_id FROM projects WHERE id = $1`, pid).Scan(&tid)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "project not found"))
		return
	}

	repoID := uuid.New()
	_, err = h.pool.Exec(r.Context(), `
		INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, repoID, tid, pid, canonRemote, wsPath, req.IsPrimary)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "insert project_repository", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":               repoID,
		"project_id":       pid,
		"canonical_remote": canonRemote,
		"workspace_path":   wsPath,
	})
}

func (h *Handler) handleTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		rows, err := h.pool.Query(ctx, `
			SELECT tk.id, tk.token_prefix, tk.token_ciphertext, tk.tenant_id, t.name, tk.principal_id, p.display_name, tk.name, tk.revoked_at, tk.created_at
			FROM api_tokens tk
			JOIN tenants t ON t.id = tk.tenant_id
			JOIN principals p ON p.id = tk.principal_id
			ORDER BY tk.created_at DESC
		`)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list api tokens", err))
			return
		}
		defer rows.Close()

		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, tid, pid uuid.UUID
			var prefix, ciphertext, tName, pName, name string
			var revokedAt *time.Time
			var createdAt time.Time
			if err := rows.Scan(&id, &prefix, &ciphertext, &tid, &tName, &pid, &pName, &name, &revokedAt, &createdAt); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "scan api token", err))
				return
			}
			canReveal := false
			if ciphertext != "" {
				if _, err := h.authMgr.UnsealToken(ciphertext); err == nil {
					canReveal = true
				}
			}
			items = append(items, map[string]any{
				"id":             id,
				"token_prefix":   prefix,
				"can_reveal":     canReveal,
				"tenant_id":      tid,
				"tenant_name":    tName,
				"principal_id":   pid,
				"principal_name": pName,
				"name":           name,
				"revoked_at":     revokedAt,
				"created_at":     createdAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})

	case http.MethodPost:
		var req struct {
			TenantID    string `json:"tenant_id"`
			PrincipalID string `json:"principal_id"`
			Name        string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
		tid, err := uuid.Parse(req.TenantID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
			return
		}
		pid, err := uuid.Parse(req.PrincipalID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid principal_id"))
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "default"
		}

		// Verify that principal is a member of the target tenant before issuing token
		var isMember bool
		err = h.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM tenant_members WHERE tenant_id = $1 AND principal_id = $2
			)
		`, tid, pid).Scan(&isMember)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "verify tenant membership for token", err))
			return
		}
		if !isMember {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "principal is not a member of the specified tenant"))
			return
		}

		rawToken, prefix, tokenHash, err := generateAPIToken()
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "generate token", err))
			return
		}
		sealedToken, err := h.authMgr.SealToken(rawToken)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "seal token", err))
			return
		}
		tokenID := uuid.New()
		_, err = h.pool.Exec(ctx, `
			INSERT INTO api_tokens (id, token_hash, token_prefix, token_ciphertext, tenant_id, principal_id, name)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, tokenID, tokenHash, prefix, sealedToken, tid, pid, strings.TrimSpace(req.Name))
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert api token", err))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":           tokenID,
			"token":        rawToken,
			"token_prefix": prefix,
			"can_reveal":   true,
			"tenant_id":    tid,
			"principal_id": pid,
			"name":         strings.TrimSpace(req.Name),
		})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleRevealToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid token id"))
		return
	}

	var ciphertext string
	err = h.pool.QueryRow(r.Context(), `
		SELECT token_ciphertext FROM api_tokens WHERE id = $1
	`, id).Scan(&ciphertext)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "token not found"))
			return
		}
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "query api token", err))
		return
	}
	if ciphertext == "" {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "该 Token 创建于支持重复查看之前，无法还原明文；请重新生成"))
		return
	}
	rawToken, err := h.authMgr.UnsealToken(ciphertext)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "无法解密该 Token（可能服务端 context_sign_key 已变更）；请重新生成"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":    id,
		"token": rawToken,
	})
}

func (h *Handler) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	id, err := uuid.Parse(req.ID)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid token id"))
		return
	}
	_, err = h.pool.Exec(r.Context(), `
		UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "revoke token", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "revoked"})
}

func (h *Handler) handleMemories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	tenantIDStr := strings.TrimSpace(q.Get("tenant_id"))
	scope := strings.TrimSpace(q.Get("scope"))
	deletedMode := strings.TrimSpace(q.Get("deleted"))
	keyword := strings.TrimSpace(q.Get("q"))

	var (
		args  []any
		where []string
	)
	addArg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if tenantIDStr != "" {
		tid, err := uuid.Parse(tenantIDStr)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
			return
		}
		where = append(where, "m.tenant_id = "+addArg(tid))
	}
	if scope == "global" || scope == "project" {
		where = append(where, "m.scope = "+addArg(scope))
	}
	switch deletedMode {
	case "active":
		where = append(where, "m.deleted_at IS NULL")
	case "deleted":
		where = append(where, "m.deleted_at IS NOT NULL")
	}
	if keyword != "" {
		likePat := "%" + escapeAdminLike(keyword) + "%"
		p := addArg(likePat)
		where = append(where, "(m.title ILIKE "+p+" OR m.content_md ILIKE "+p+")")
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	rows, err := h.pool.Query(r.Context(), `
		SELECT m.id, m.tenant_id, m.scope, m.project_id, coalesce(p.name, ''),
		       m.category, m.title, m.content_md, m.tags, m.is_pinned, m.status,
		       m.version, m.updated_at, m.deleted_at
		FROM memories m
		LEFT JOIN projects p ON p.id = m.project_id
		`+whereClause+`
		ORDER BY m.updated_at DESC
		LIMIT 100
	`, args...)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list memories", err))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, tid                 uuid.UUID
			projID                  *uuid.UUID
			sc, pName, cat, title   string
			content, status         string
			tags                    []string
			isPinned                bool
			version                 int64
			updatedAt               time.Time
			deletedAt               *time.Time
		)
		if err := rows.Scan(&id, &tid, &sc, &projID, &pName, &cat, &title, &content, &tags, &isPinned, &status, &version, &updatedAt, &deletedAt); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "scan memory", err))
			return
		}
		items = append(items, map[string]any{
			"id":           id,
			"tenant_id":    tid,
			"scope":        sc,
			"project_id":   projID,
			"project_name": pName,
			"category":     cat,
			"title":        title,
			"content_md":   content,
			"tags":         tags,
			"is_pinned":    isPinned,
			"status":       status,
			"version":      version,
			"updated_at":   updatedAt,
			"deleted_at":   deletedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleMemoryRevisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	memID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("memory_id")))
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id"))
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT version, operation, snapshot, actor_id, request_id, created_at
		FROM memory_revisions
		WHERE memory_id = $1
		ORDER BY version DESC
	`, memID)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "query revisions", err))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var version int64
		var op, reqID string
		var snapBytes []byte
		var actorID uuid.UUID
		var createdAt time.Time
		if err := rows.Scan(&version, &op, &snapBytes, &actorID, &reqID, &createdAt); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "scan revision", err))
			return
		}
		var snap any
		_ = json.Unmarshal(snapBytes, &snap)
		items = append(items, map[string]any{
			"version":    version,
			"operation":  op,
			"snapshot":   snap,
			"actor_id":   actorID,
			"request_id": reqID,
			"created_at": createdAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleRestoreMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TenantID        string `json:"tenant_id"`
		ActorID         string `json:"actor_id"`
		MemoryID        string `json:"memory_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	tid, err := uuid.Parse(req.TenantID)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
		return
	}
	memID, err := uuid.Parse(req.MemoryID)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id"))
		return
	}

	var actorID uuid.UUID
	if strings.TrimSpace(req.ActorID) != "" {
		actorID, err = uuid.Parse(req.ActorID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid actor_id"))
			return
		}
	} else {
		actorID, err = h.ensureAdminActorForTenant(r.Context(), tid)
		if err != nil {
			writeJSONError(w, err)
			return
		}
	}

	restored, err := h.memSvc.Restore(r.Context(), memory.RestoreRequest{
		TenantID:        tid,
		ActorID:         actorID,
		MemoryID:        memID,
		ExpectedVersion: req.ExpectedVersion,
		RequestID:       "webui-restore-" + uuid.NewString(),
	})
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, restored)
}

func (h *Handler) ensureAdminActorForTenant(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, error) {
	var actorID uuid.UUID
	err := h.pool.QueryRow(ctx, `
		SELECT principal_id FROM tenant_members
		WHERE tenant_id = $1 AND role = 'admin'
		LIMIT 1
	`, tenantID).Scan(&actorID)
	if err == nil {
		return actorID, nil
	}

	err = h.pool.QueryRow(ctx, `
		INSERT INTO principals (id, identity_provider, subject, display_name, status)
		VALUES ($1, 'system_webui', 'admin', 'Web UI System Admin', 'active')
		ON CONFLICT (identity_provider, subject) DO UPDATE SET status = 'active'
		RETURNING id
	`, uuid.New()).Scan(&actorID)
	if err != nil {
		return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "ensure system admin principal", err)
	}

	_, err = h.pool.Exec(ctx, `
		INSERT INTO tenant_members (tenant_id, principal_id, role)
		VALUES ($1, $2, 'admin')
		ON CONFLICT (tenant_id, principal_id) DO UPDATE SET role = 'admin'
	`, tenantID, actorID)
	if err != nil {
		return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "ensure system admin tenant membership", err)
	}
	return actorID, nil
}

func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	count, err := h.exporter.ExportAll(r.Context())
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "export markdown failed: "+err.Error(), err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exported_count": count,
		"output_dir":     h.exporter.OutputDir(),
	})
}

func (h *Handler) handleExportArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var buf bytes.Buffer
	count, err := h.exporter.ExportArchive(r.Context(), &buf)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "export markdown archive failed: "+err.Error(), err))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="engram-markdown-export.zip"`)
	w.Header().Set("X-Exported-Count", fmt.Sprintf("%d", count))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func escapeAdminLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func generateAPIToken() (rawToken, prefix, tokenHash string, err error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", err
	}
	rawToken = "aim_" + hex.EncodeToString(b)
	prefix = rawToken[:12]
	tokenHash = auth.HashToken(rawToken)
	return rawToken, prefix, tokenHash, nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, err error) {
	appErr := apperror.AsError(err, "admin_"+uuid.NewString())
	writeJSON(w, appErr.HTTPStatus(), appErr)
}
