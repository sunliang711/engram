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
	"strconv"
	"strings"
	"time"

	"engram/internal/apperror"
	"engram/internal/audit"
	"engram/internal/auth"
	"engram/internal/exporter"
	"engram/internal/memory"
	"engram/internal/observability"
	"engram/internal/project"
	"engram/internal/safekeep"
	"engram/internal/scheduler"
	"engram/internal/version"

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
	auditSvc     *audit.Service
	scheduler    *scheduler.Scheduler
	safekeepSvc  *safekeep.Service
	loginLimiter *auth.RateLimiter
	log          zerolog.Logger
}

// SetAuditService attaches the audit module to record and query console audit logs and tasks.
func (h *Handler) SetAuditService(svc *audit.Service) {
	h.auditSvc = svc
}

// SetScheduler attaches the scheduled task module to manage and trigger background jobs.
func (h *Handler) SetScheduler(sched *scheduler.Scheduler) {
	h.scheduler = sched
}

// SetSafekeepService attaches the SafeKeep remote backup integration service.
func (h *Handler) SetSafekeepService(svc *safekeep.Service) {
	h.safekeepSvc = svc
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
		if !isUIRoutePath(r.URL.Path) {
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
	mux.HandleFunc("/api/admin/memories/delete", h.requireActiveAdmin(h.handleDeleteMemory))
	mux.HandleFunc("/api/admin/memories/pin", h.requireActiveAdmin(h.handlePinMemory))
	mux.HandleFunc("/api/admin/memories/facets", h.requireActiveAdmin(h.handleMemoryFacets))
	mux.HandleFunc("/api/admin/memories/purge/backup", h.requireActiveAdmin(h.handlePurgeBackup))
	mux.HandleFunc("/api/admin/memories/purge", h.requireActiveAdmin(h.handlePurge))
	mux.HandleFunc("/api/admin/export", h.requireActiveAdmin(h.handleExport))
	mux.HandleFunc("/api/admin/export/archive", h.requireActiveAdmin(h.handleExportArchive))
	mux.HandleFunc("/api/admin/migration/export", h.requireActiveAdmin(h.handleMigrationExport))
	mux.HandleFunc("/api/admin/migration/inspect", h.requireActiveAdminMultipart(maxImportBytes, h.handleMigrationInspect))
	mux.HandleFunc("/api/admin/migration/import", h.requireActiveAdminMultipart(maxImportBytes, h.handleMigrationImport))

	// Audit & Scheduled Tasks APIs
	mux.HandleFunc("/api/admin/audit/logs", h.requireActiveAdmin(h.handleAuditLogs))
	mux.HandleFunc("/api/admin/audit/logs/export", h.requireActiveAdmin(h.handleAuditLogsExport))
	mux.HandleFunc("/api/admin/audit/stats", h.requireActiveAdmin(h.handleAuditStats))
	mux.HandleFunc("/api/admin/audit/cleanup", h.requireActiveAdmin(h.handleAuditCleanup))
	mux.HandleFunc("/api/admin/audit/tasks", h.requireActiveAdmin(h.handleAuditTasks))
	mux.HandleFunc("/api/admin/audit/tasks/run", h.requireActiveAdmin(h.handleAuditTaskRun))
	mux.HandleFunc("/api/admin/scheduler/tasks", h.requireActiveAdmin(h.handleSchedulerTasks))
	mux.HandleFunc("/api/admin/scheduler/tasks/run", h.requireActiveAdmin(h.handleSchedulerTaskRun))
	mux.HandleFunc("/api/admin/scheduler/runs", h.requireActiveAdmin(h.handleSchedulerRuns))
	mux.HandleFunc("/api/admin/integrations/safekeep", h.requireActiveAdmin(h.handleSafekeepSettings))
	mux.HandleFunc("/api/admin/integrations/safekeep/reveal", h.requireActiveAdmin(h.handleSafekeepReveal))
	mux.HandleFunc("/api/admin/integrations/safekeep/test", h.requireActiveAdmin(h.handleSafekeepTest))
	mux.HandleFunc("/api/admin/integrations/safekeep/backup", h.requireActiveAdmin(h.handleSafekeepBackupNow))
}

// isUIRoutePath matches Web UI SPA paths so browser refreshes and deep links serve index.html
// without shadowing unknown API or asset paths.
func isUIRoutePath(p string) bool {
	if p == "/" || p == "/overview" || p == "/memories" || p == "/projects" || p == "/tenants" || p == "/migration" || p == "/audit" || p == "/scheduler" || p == "/settings" {
		return true
	}
	trimmed := strings.Trim(p, "/")
	parts := strings.Split(trimmed, "/")
	switch parts[0] {
	case "memories", "projects":
		return len(parts) == 2 && parts[1] != ""
	case "tenants":
		if len(parts) == 2 && parts[1] != "" {
			return true
		}
		return len(parts) == 4 && parts[1] != "" && parts[2] == "members" && parts[3] != ""
	default:
		return false
	}
}

func (h *Handler) withSafety(next http.HandlerFunc) http.HandlerFunc {
	return h.withSafetyOpts(1<<20, false, next)
}

func (h *Handler) withSafetyOpts(maxBytes int64, allowMultipart bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

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
				isJSON := strings.HasPrefix(ct, "application/json")
				isMultipart := allowMultipart && strings.HasPrefix(ct, "multipart/form-data")
				if !isJSON && !isMultipart {
					writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "unsupported Content-Type"))
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
	return h.withSafety(h.requireActiveAdminCore(next))
}

func (h *Handler) requireActiveAdminMultipart(maxBytes int64, next http.HandlerFunc) http.HandlerFunc {
	return h.withSafetyOpts(maxBytes, true, h.requireActiveAdminCore(next))
}

type adminUserCtxKey struct{}

func adminActorFromRequest(r *http.Request) string {
	if r == nil {
		return "admin"
	}
	if name, ok := r.Context().Value(adminUserCtxKey{}).(string); ok && strings.TrimSpace(name) != "" {
		return name
	}
	return "admin"
}

func requestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (h *Handler) recordAdminAudit(r *http.Request, entry audit.Entry) {
	if h == nil || h.auditSvc == nil {
		return
	}
	if entry.ActorType == "" {
		entry.ActorType = audit.ActorAdmin
	}
	if entry.ActorName == "" {
		entry.ActorName = adminActorFromRequest(r)
	}
	if entry.Source == "" {
		entry.Source = audit.SourceAdminUI
	}
	if entry.ClientIP == "" {
		entry.ClientIP = requestClientIP(r)
	}
	if entry.Status == "" {
		entry.Status = audit.StatusSuccess
	}
	h.auditSvc.Record(r.Context(), entry)
}

func (h *Handler) requireActiveAdminCore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		ctx := context.WithValue(r.Context(), adminUserCtxKey{}, state.Username)
		next(w, r.WithContext(ctx))
	}
}

func withSessionVersion(state AdminState) AdminState {
	state.Version = version.Current()
	return state
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := h.extractSessionToken(r)
	state, err := h.authMgr.VerifySession(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusOK, withSessionVersion(AdminState{
			Authenticated:       false,
			ResetAdminEnvActive: state.ResetAdminEnvActive,
		}))
		return
	}
	writeJSON(w, http.StatusOK, withSessionVersion(state))
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
		h.recordAdminAudit(r, audit.Entry{
			ActorName:    "admin",
			Action:       "admin.login",
			ResourceType: "admin_session",
			Status:       audit.StatusDenied,
			ErrorCode:    string(apperror.CodeRateLimited),
			ErrorMessage: err.Error(),
		})
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
		appErr := apperror.AsError(err, "")
		h.recordAdminAudit(r, audit.Entry{
			ActorName:    strings.TrimSpace(req.Username),
			Action:       "admin.login",
			ResourceType: "admin_session",
			Status:       audit.StatusFailed,
			ErrorCode:    string(appErr.Code),
			ErrorMessage: appErr.Message,
		})
		writeJSONError(w, err)
		return
	}
	h.setSessionCookie(w, r, token)
	h.recordAdminAudit(r, audit.Entry{
		ActorName:    state.Username,
		Action:       "admin.login",
		ResourceType: "admin_session",
		Status:       audit.StatusSuccess,
	})
	writeJSON(w, http.StatusOK, withSessionVersion(state))
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	username := "admin"
	if state, err := h.authMgr.VerifySession(r.Context(), h.extractSessionToken(r)); err == nil && state.Authenticated && state.Username != "" {
		username = state.Username
	}
	h.clearSessionCookie(w, r)
	h.recordAdminAudit(r, audit.Entry{
		ActorName:    username,
		Action:       "admin.logout",
		ResourceType: "admin_session",
		Status:       audit.StatusSuccess,
	})
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
		appErr := apperror.AsError(err, "")
		h.recordAdminAudit(r, audit.Entry{
			ActorName:    state.Username,
			Action:       "admin.password_change",
			ResourceType: "admin_account",
			ResourceID:   state.Username,
			Status:       audit.StatusFailed,
			ErrorCode:    string(appErr.Code),
			ErrorMessage: appErr.Message,
		})
		writeJSONError(w, err)
		return
	}
	h.setSessionCookie(w, r, newToken)
	h.recordAdminAudit(r, audit.Entry{
		ActorName:    state.Username,
		Action:       "admin.password_change",
		ResourceType: "admin_account",
		ResourceID:   state.Username,
		Status:       audit.StatusSuccess,
	})
	writeJSON(w, http.StatusOK, withSessionVersion(newState))
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
			(SELECT count(*)::int FROM principals WHERE identity_provider <> 'system_webui'),
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

// handleQuickstart provisions a member with independent global and project grants.
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
		CanCreateProject     *bool  `json:"can_create_project"`
		ProjectName          string `json:"project_name"`
		RemoteURL            string `json:"remote_url"`
		WorkspacePath        string `json:"workspace_path"`
		ClientName           string `json:"client_name"`
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
	if req.TenantRole != "none" && req.TenantRole != "member" && req.TenantRole != "global_writer" {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid global permission"))
		return
	}
	if req.TenantName == "" || req.PrincipalSubject == "" || req.PrincipalDisplayName == "" {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "tenant_name, principal_subject, and principal_display_name are required"))
		return
	}

	clientName := strings.TrimSpace(req.ClientName)
	if clientName == "" {
		clientName = "quickstart-" + req.PrincipalSubject
	}

	var canonRemote, wsPath string
	var reused, remoteElsewhere bool
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

	// Lock before principal or membership writes to keep the MCP lock order consistent.
	if err := project.LockRegistration(ctx, tx, tenantID); err != nil {
		writeJSONError(w, err)
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
	// Omitted creation permission preserves existing grants for older clients.
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_members (tenant_id, principal_id, role, can_create_project)
		VALUES ($1, $2, $3, COALESCE($4, false))
		ON CONFLICT (tenant_id, principal_id) DO UPDATE SET role = EXCLUDED.role, can_create_project = COALESCE($4, tenant_members.can_create_project)
	`, tenantID, principalID, req.TenantRole, req.CanCreateProject); err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "assign tenant role", err))
		return
	}

	// 4. Optional project + repository + project_member. Resubmitting the wizard reuses
	// the active project of the same name so repeated runs never create look-alike projects.
	var projectID *uuid.UUID
	var markerUUID *uuid.UUID
	if name := strings.TrimSpace(req.ProjectName); name != "" {
		pid, err := findActiveProjectByName(ctx, tx, tenantID, name)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		if pid == uuid.Nil {
			pid = uuid.New()
			m := uuid.New()
			markerUUID = &m
			if _, err := tx.Exec(ctx, `
				INSERT INTO projects (id, tenant_id, name, marker, status)
				VALUES ($1, $2, $3, $4, 'active')
			`, pid, tenantID, name, m); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert project", err))
				return
			}
		} else {
			reused = true
			if err := tx.QueryRow(ctx, `SELECT marker FROM projects WHERE id = $1`, pid).Scan(&markerUUID); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "load project marker", err))
				return
			}
		}
		projectID = &pid
		if _, err := tx.Exec(ctx, `
			INSERT INTO project_members (project_id, principal_id, role)
			VALUES ($1, $2, 'writer')
			ON CONFLICT (project_id, principal_id) DO UPDATE SET role = 'writer'
		`, pid, principalID); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert project_member", err))
			return
		}
		if canonRemote != "" {
			// A remote mapped to another project stays with that project; the
			// response reports where the mapping actually lives.
			var mappedProject uuid.UUID
			err := tx.QueryRow(ctx, `
				INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary)
				VALUES ($1, $2, $3, $4, $5, NOT EXISTS (SELECT 1 FROM project_repositories WHERE project_id = $3))
				ON CONFLICT (tenant_id, canonical_remote, workspace_path) DO UPDATE SET canonical_remote = EXCLUDED.canonical_remote
				RETURNING project_id
			`, uuid.New(), tenantID, pid, canonRemote, wsPath).Scan(&mappedProject)
			if err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "insert project_repository", err))
				return
			}
			remoteElsewhere = mappedProject != pid
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
	`, tokenID, tokenHash, prefix, sealedToken, tenantID, principalID, clientName); err != nil {
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
		"token_name":       clientName,
		"project_reused":   reused,
		"remote_elsewhere": remoteElsewhere,
	})
}

// findActiveProjectByName returns the oldest active project with the given name in a tenant,
// or uuid.Nil when none exists.
func findActiveProjectByName(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenantID uuid.UUID, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT id FROM projects
		WHERE tenant_id = $1 AND name = $2 AND status = 'active'
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`, tenantID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "query project by name", err)
	}
	return id, nil
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
		// Quickstart finds tenants by name, so names stay unique to keep that lookup unambiguous.
		var exists bool
		if err := h.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE name = $1)`, strings.TrimSpace(req.Name)).Scan(&exists); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "check tenant name", err))
			return
		}
		if exists {
			writeJSON(w, http.StatusConflict, map[string]any{"code": "TENANT_EXISTS", "message": "已有同名租户，请换一个名称。"})
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
		h.recordAdminAudit(r, audit.Entry{
			TenantID:     &id,
			Action:       "tenant.create",
			ResourceType: "tenant",
			ResourceID:   id.String(),
			Details:      map[string]any{"name": strings.TrimSpace(req.Name)},
		})
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": strings.TrimSpace(req.Name), "status": "active"})

	case http.MethodPatch:
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
		id, err := uuid.Parse(strings.TrimSpace(req.ID))
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant id"))
			return
		}
		newName := strings.TrimSpace(req.Name)
		if newName == "" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "tenant name is required"))
			return
		}

		tx, err := h.pool.Begin(ctx)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin rename tenant tx", err))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE id = $1 FOR UPDATE)`, id).Scan(&exists); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "check tenant exists", err))
			return
		}
		if !exists {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "tenant not found"))
			return
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenants WHERE name = $1 AND id <> $2)`, newName, id).Scan(&dup); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "check tenant name", err))
			return
		}
		if dup {
			writeJSON(w, http.StatusConflict, map[string]any{"code": "TENANT_EXISTS", "message": "已有同名租户，请换一个名称。"})
			return
		}
		var status string
		var createdAt time.Time
		if err := tx.QueryRow(ctx, `
			UPDATE tenants SET name = $2 WHERE id = $1
			RETURNING status, created_at
		`, id, newName).Scan(&status, &createdAt); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "rename tenant", err))
			return
		}
		if err := tx.Commit(ctx); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit rename tenant tx", err))
			return
		}
		h.recordAdminAudit(r, audit.Entry{
			TenantID:     &id,
			Action:       "tenant.update",
			ResourceType: "tenant",
			ResourceID:   id.String(),
			Details:      map[string]any{"name": newName},
		})
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": newName, "status": status, "created_at": createdAt})

	case http.MethodDelete:
		var req struct {
			ID string `json:"id"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
				return
			}
		}
		rawID := strings.TrimSpace(req.ID)
		if rawID == "" {
			rawID = strings.TrimSpace(r.URL.Query().Get("id"))
		}
		id, err := uuid.Parse(rawID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant id"))
			return
		}

		tx, err := h.pool.Begin(ctx)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin delete tenant tx", err))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var tenantName string
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1 FOR UPDATE`, id).Scan(&tenantName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "tenant not found"))
				return
			}
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "lock tenant", err))
			return
		}

		for _, stmt := range []struct {
			sql string
			msg string
		}{
			{`DELETE FROM idempotency_requests WHERE tenant_id = $1`, "delete tenant idempotency_requests"},
			{`DELETE FROM memory_events WHERE tenant_id = $1`, "delete tenant memory_events"},
			{`DELETE FROM memory_revisions WHERE tenant_id = $1`, "delete tenant memory_revisions"},
			{`UPDATE memories SET source_memory_id = NULL WHERE source_memory_id IN (SELECT id FROM memories WHERE tenant_id = $1)`, "unlink tenant source_memory_id"},
			{`DELETE FROM memories WHERE tenant_id = $1`, "delete tenant memories"},
			{`DELETE FROM project_repositories WHERE tenant_id = $1`, "delete tenant project_repositories"},
			{`DELETE FROM project_members WHERE project_id IN (SELECT id FROM projects WHERE tenant_id = $1)`, "delete tenant project_members"},
			{`DELETE FROM projects WHERE tenant_id = $1`, "delete tenant projects"},
			{`DELETE FROM api_tokens WHERE tenant_id = $1`, "delete tenant api_tokens"},
			{`DELETE FROM tenant_members WHERE tenant_id = $1`, "delete tenant memberships"},
			{`DELETE FROM tenants WHERE id = $1`, "delete tenant"},
		} {
			if _, err := tx.Exec(ctx, stmt.sql, id); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, stmt.msg, err))
				return
			}
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit delete tenant tx", err))
			return
		}
		h.recordAdminAudit(r, audit.Entry{
			TenantID:     &id,
			Action:       "tenant.delete",
			ResourceType: "tenant",
			ResourceID:   id.String(),
			Details:      map[string]any{"name": tenantName},
		})
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id, "name": tenantName})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handlePrincipals lists identities and separates creation, binding, and explicit role changes.
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
			SELECT tm.principal_id, tm.tenant_id, t.name, tm.role, tm.can_create_project
			FROM tenant_members tm
			JOIN tenants t ON t.id = tm.tenant_id
		`)
		if err == nil {
			defer tmRows.Close()
			for tmRows.Next() {
				var pid, tid uuid.UUID
				var tName, role string
				var canCreateProject bool
				if err := tmRows.Scan(&pid, &tid, &tName, &role, &canCreateProject); err == nil {
					if p, ok := byID[pid]; ok {
						p.TenantRoles = append(p.TenantRoles, map[string]any{
							"tenant_id":          tid,
							"tenant_name":        tName,
							"role":               role,
							"can_create_project": canCreateProject,
						})
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": list})

	case http.MethodPost:
		var req struct {
			Mode             string `json:"mode"`
			PrincipalID      string `json:"principal_id"`
			TenantID         string `json:"tenant_id"`
			IdentityProvider string `json:"identity_provider"`
			Subject          string `json:"subject"`
			DisplayName      string `json:"display_name"`
			TenantRole       string `json:"tenant_role"`
			CanCreateProject *bool  `json:"can_create_project"`
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
		if req.Mode == "" {
			req.Mode = "create"
		}
		if req.Mode != "create" && req.Mode != "bind" && req.Mode != "update_role" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid principal mode"))
			return
		}
		if req.Mode == "create" && (strings.TrimSpace(req.Subject) == "" || strings.TrimSpace(req.DisplayName) == "") {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "subject and display_name are required"))
			return
		}
		if req.TenantRole == "" {
			req.TenantRole = "none"
		}
		if req.TenantRole != "member" && req.TenantRole != "global_writer" && req.TenantRole != "none" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant role"))
			return
		}
		if req.IdentityProvider != "api_token" && req.IdentityProvider != "oidc" && req.IdentityProvider != "mtls" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid identity provider"))
			return
		}

		tx, err := h.pool.Begin(ctx)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin tx", err))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var pid uuid.UUID
		if req.Mode == "create" {
			err = tx.QueryRow(ctx, `
			INSERT INTO principals (id, identity_provider, subject, display_name, status)
			VALUES ($1, $2, $3, $4, 'active')
			ON CONFLICT (identity_provider, subject) DO NOTHING
			RETURNING id
		`, uuid.New(), strings.TrimSpace(req.IdentityProvider), strings.TrimSpace(req.Subject), strings.TrimSpace(req.DisplayName)).Scan(&pid)
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusConflict, map[string]any{"code": "PRINCIPAL_EXISTS", "message": "成员标识已存在，请选择绑定已有成员；原有名称和权限未变更。"})
				return
			}
		} else {
			pid, err = uuid.Parse(req.PrincipalID)
			if err != nil {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "请选择有效的已有成员"))
				return
			}
			// Binding an existing identity never changes its profile or another tenant's role.
			err = tx.QueryRow(ctx, `SELECT id FROM principals WHERE id = $1 AND status = 'active' AND identity_provider <> 'system_webui' FOR UPDATE`, pid).Scan(&pid)
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "请选择有效的已有成员"))
				return
			}
		}
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "resolve principal", err))
			return
		}

		var affected int64
		if req.Mode == "update_role" {
			// Updating global access alone must not revoke the independent creation grant.
			tag, updateErr := tx.Exec(ctx, `UPDATE tenant_members SET role = $3, can_create_project = COALESCE($4, can_create_project) WHERE tenant_id = $1 AND principal_id = $2`, tid, pid, req.TenantRole, req.CanCreateProject)
			err, affected = updateErr, tag.RowsAffected()
		} else {
			tag, bindErr := tx.Exec(ctx, `
			INSERT INTO tenant_members (tenant_id, principal_id, role, can_create_project)
			VALUES ($1, $2, $3, COALESCE($4, false))
			ON CONFLICT (tenant_id, principal_id) DO NOTHING
		`, tid, pid, req.TenantRole, req.CanCreateProject)
			err, affected = bindErr, tag.RowsAffected()
		}
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "bind tenant role", err))
			return
		}
		if affected == 0 {
			writeJSON(w, http.StatusConflict, map[string]any{"code": "MEMBERSHIP_CONFLICT", "message": "成员绑定状态已变化；请刷新后选择绑定或调整角色，已有权限未变更。"})
			return
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit principal tx", err))
			return
		}
		status := http.StatusCreated
		if req.Mode == "update_role" {
			status = http.StatusOK
		}
		action := "principal." + req.Mode
		if req.Mode != "create" && req.Mode != "bind" && req.Mode != "update_role" {
			action = "principal.upsert"
		}
		h.recordAdminAudit(r, audit.Entry{
			TenantID:     &tid,
			Action:       action,
			ResourceType: "principal",
			ResourceID:   pid.String(),
			Details: map[string]any{
				"mode":         req.Mode,
				"tenant_role":  req.TenantRole,
				"display_name": strings.TrimSpace(req.DisplayName),
			},
		})
		writeJSON(w, status, map[string]any{"principal_id": pid, "tenant_id": tid, "role": req.TenantRole, "mode": req.Mode})

	case http.MethodPatch:
		var req struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
		pid, err := uuid.Parse(strings.TrimSpace(req.ID))
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid principal id"))
			return
		}
		displayName := strings.TrimSpace(req.DisplayName)
		if displayName == "" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "display_name is required"))
			return
		}

		var provider string
		if err := h.pool.QueryRow(ctx, `SELECT identity_provider FROM principals WHERE id = $1`, pid).Scan(&provider); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "principal not found"))
				return
			}
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "query principal", err))
			return
		}
		if provider == "system_webui" {
			writeJSONError(w, apperror.New(apperror.CodeUnauthorized, "cannot modify system principal"))
			return
		}

		var subject, status string
		if err := h.pool.QueryRow(ctx, `
			UPDATE principals SET display_name = $2 WHERE id = $1
			RETURNING subject, status
		`, pid, displayName).Scan(&subject, &status); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "rename principal", err))
			return
		}
		h.recordAdminAudit(r, audit.Entry{
			Action:       "principal.update",
			ResourceType: "principal",
			ResourceID:   pid.String(),
			Details:      map[string]any{"display_name": displayName},
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"id":                pid,
			"identity_provider": provider,
			"subject":           subject,
			"display_name":      displayName,
			"status":            status,
		})

	case http.MethodDelete:
		var req struct {
			ID       string `json:"id"`
			TenantID string `json:"tenant_id"`
			Scope    string `json:"scope"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
				return
			}
		}
		rawID := strings.TrimSpace(req.ID)
		if rawID == "" {
			rawID = strings.TrimSpace(r.URL.Query().Get("id"))
		}
		pid, err := uuid.Parse(rawID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid principal id"))
			return
		}
		scope := strings.TrimSpace(req.Scope)
		if scope == "" {
			scope = strings.TrimSpace(r.URL.Query().Get("scope"))
		}
		if scope == "" {
			scope = "all"
		}
		if scope != "all" && scope != "tenant" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid delete scope"))
			return
		}

		tx, err := h.pool.Begin(ctx)
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin delete principal tx", err))
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var provider, displayName string
		if err := tx.QueryRow(ctx, `SELECT identity_provider, display_name FROM principals WHERE id = $1 FOR UPDATE`, pid).Scan(&provider, &displayName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "principal not found"))
				return
			}
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "lock principal", err))
			return
		}
		if provider == "system_webui" {
			writeJSONError(w, apperror.New(apperror.CodeUnauthorized, "cannot delete system principal"))
			return
		}

		if scope == "tenant" {
			rawTID := strings.TrimSpace(req.TenantID)
			if rawTID == "" {
				rawTID = strings.TrimSpace(r.URL.Query().Get("tenant_id"))
			}
			tid, parseErr := uuid.Parse(rawTID)
			if parseErr != nil {
				writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
				return
			}
			for _, stmt := range []struct {
				sql string
				msg string
			}{
				{`DELETE FROM idempotency_requests WHERE tenant_id = $1 AND principal_id = $2`, "delete tenant principal idempotency_requests"},
				{`DELETE FROM api_tokens WHERE tenant_id = $1 AND principal_id = $2`, "delete tenant principal api_tokens"},
				{`DELETE FROM project_members WHERE principal_id = $2 AND project_id IN (SELECT id FROM projects WHERE tenant_id = $1)`, "delete tenant principal project_members"},
				{`DELETE FROM tenant_members WHERE tenant_id = $1 AND principal_id = $2`, "remove tenant membership"},
			} {
				if _, err := tx.Exec(ctx, stmt.sql, tid, pid); err != nil {
					writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, stmt.msg, err))
					return
				}
			}
			if err := tx.Commit(ctx); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit remove principal from tenant tx", err))
				return
			}
			h.recordAdminAudit(r, audit.Entry{
				TenantID:     &tid,
				Action:       "principal.remove_from_tenant",
				ResourceType: "principal",
				ResourceID:   pid.String(),
				Details:      map[string]any{"display_name": displayName},
			})
			writeJSON(w, http.StatusOK, map[string]any{"status": "removed_from_tenant", "id": pid, "tenant_id": tid})
			return
		}

		// Preserve historical memories, revisions, and events under the system console principal.
		var systemPrincipalID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO principals (id, identity_provider, subject, display_name, status)
			VALUES ($1, 'system_webui', 'admin', 'Web UI System Admin', 'active')
			ON CONFLICT (identity_provider, subject) DO UPDATE SET status = 'active'
			RETURNING id
		`, uuid.New()).Scan(&systemPrincipalID); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "ensure system admin principal", err))
			return
		}
		for _, stmt := range []struct {
			sql  string
			args []any
			msg  string
		}{
			{`UPDATE memories SET created_by = $2 WHERE created_by = $1`, []any{pid, systemPrincipalID}, "reassign memory created_by"},
			{`UPDATE memories SET updated_by = $2 WHERE updated_by = $1`, []any{pid, systemPrincipalID}, "reassign memory updated_by"},
			{`UPDATE memory_revisions SET actor_id = $2 WHERE actor_id = $1`, []any{pid, systemPrincipalID}, "reassign memory_revisions actor_id"},
			{`UPDATE memory_events SET actor_id = $2 WHERE actor_id = $1`, []any{pid, systemPrincipalID}, "reassign memory_events actor_id"},
			{`DELETE FROM idempotency_requests WHERE principal_id = $1`, []any{pid}, "delete principal idempotency_requests"},
			{`DELETE FROM api_tokens WHERE principal_id = $1`, []any{pid}, "delete principal api_tokens"},
			{`DELETE FROM project_members WHERE principal_id = $1`, []any{pid}, "delete principal project_members"},
			{`DELETE FROM tenant_members WHERE principal_id = $1`, []any{pid}, "delete principal tenant_members"},
			{`DELETE FROM principals WHERE id = $1`, []any{pid}, "delete principal"},
		} {
			if _, err := tx.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, stmt.msg, err))
				return
			}
		}

		if err := tx.Commit(ctx); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit delete principal tx", err))
			return
		}
		h.recordAdminAudit(r, audit.Entry{
			Action:       "principal.delete",
			ResourceType: "principal",
			ResourceID:   pid.String(),
			Details:      map[string]any{"display_name": displayName, "scope": scope},
		})
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": pid, "display_name": displayName})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleProjects lists projects and serializes administrator registration with MCP.
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

		if err := project.LockRegistration(ctx, tx, tid); err != nil {
			writeJSONError(w, err)
			return
		}

		if existing, err := findActiveProjectByName(ctx, tx, tid, strings.TrimSpace(req.Name)); err != nil {
			writeJSONError(w, err)
			return
		} else if existing != uuid.Nil {
			writeJSON(w, http.StatusConflict, map[string]any{"code": "PROJECT_EXISTS", "message": "该租户下已有同名项目；如需关联新的仓库地址，请在已有项目上添加仓库映射。"})
			return
		}

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

		h.recordAdminAudit(r, audit.Entry{
			TenantID:     &tid,
			ProjectID:    &projID,
			Action:       "project.create",
			ResourceType: "project",
			ResourceID:   projID.String(),
			Details: map[string]any{
				"name":             strings.TrimSpace(req.Name),
				"canonical_remote": observability.SanitizeRemoteURL(canonRemote),
				"workspace_path":   wsPath,
			},
		})

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

// handleProjectMembers grants, changes, or revokes access to one project.
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

	if req.Role != "none" && req.Role != "reader" && req.Role != "writer" {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid project permission"))
		return
	}

	// Verify that principal belongs to the same tenant as the target project
	var belongsToTenant bool
	err = h.pool.QueryRow(r.Context(), `
		SELECT EXISTS (
			SELECT 1
			FROM projects p
			JOIN tenant_members tm ON tm.tenant_id = p.tenant_id
			JOIN principals pr ON pr.id = tm.principal_id AND pr.identity_provider <> 'system_webui'
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

	if req.Role == "none" {
		if _, err := h.pool.Exec(r.Context(), `DELETE FROM project_members WHERE project_id = $1 AND principal_id = $2`, pid, prinID); err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "revoke project permission", err))
			return
		}
		h.recordAdminAudit(r, audit.Entry{
			ProjectID:    &pid,
			Action:       "project.member_revoke",
			ResourceType: "project_member",
			ResourceID:   prinID.String(),
			Details:      map[string]any{"project_id": pid.String(), "role": "none"},
		})
		writeJSON(w, http.StatusOK, map[string]any{"project_id": pid, "principal_id": prinID, "role": "none"})
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
	h.recordAdminAudit(r, audit.Entry{
		ProjectID:    &pid,
		Action:       "project.member_upsert",
		ResourceType: "project_member",
		ResourceID:   prinID.String(),
		Details:      map[string]any{"project_id": pid.String(), "role": req.Role},
	})
	writeJSON(w, http.StatusOK, map[string]any{"project_id": pid, "principal_id": prinID, "role": req.Role})
}

// handleProjectRemotes adds repository mappings under the same tenant registration lock as MCP.
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

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "begin repository registration tx", err))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := project.LockRegistration(ctx, tx, tid); err != nil {
		writeJSONError(w, err)
		return
	}

	repoID := uuid.New()
	tag, err := tx.Exec(ctx, `
		INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, canonical_remote, workspace_path) DO NOTHING
	`, repoID, tid, pid, canonRemote, wsPath, req.IsPrimary)
	if err == nil && tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "REPOSITORY_EXISTS", "message": "该仓库地址（含子目录）已关联到本租户的项目，无需重复添加。"})
		return
	}
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "insert project_repository", err))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "commit repository registration", err))
		return
	}
	h.recordAdminAudit(r, audit.Entry{
		TenantID:     &tid,
		ProjectID:    &pid,
		Action:       "project.remote_add",
		ResourceType: "project_repository",
		ResourceID:   repoID.String(),
		Details: map[string]any{
			"canonical_remote": observability.SanitizeRemoteURL(canonRemote),
			"workspace_path":   wsPath,
		},
	})
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
			SELECT tk.id, tk.token_prefix, tk.token_ciphertext, tk.tenant_id, t.name, tk.principal_id, p.display_name, tk.name, tk.revoked_at, tk.created_at, tk.last_used_at
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
			var revokedAt, lastUsedAt *time.Time
			var createdAt time.Time
			if err := rows.Scan(&id, &prefix, &ciphertext, &tid, &tName, &pid, &pName, &name, &revokedAt, &createdAt, &lastUsedAt); err != nil {
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
				"last_used_at":   lastUsedAt,
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
				SELECT 1 FROM tenant_members tm JOIN principals pr ON pr.id = tm.principal_id
				WHERE tm.tenant_id = $1 AND tm.principal_id = $2 AND pr.identity_provider <> 'system_webui'
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
		h.recordAdminAudit(r, audit.Entry{
			TenantID:     &tid,
			Action:       "token.issue",
			ResourceType: "api_token",
			ResourceID:   tokenID.String(),
			Details: map[string]any{
				"principal_id": pid.String(),
				"token_prefix": prefix,
				"token_name":   strings.TrimSpace(req.Name),
			},
		})
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

// handleRevealToken returns an existing client key only for an explicit admin action.
func (h *Handler) handleRevealToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	h.recordAdminAudit(r, audit.Entry{
		Action:       "token.reveal",
		ResourceType: "api_token",
		ResourceID:   id.String(),
	})
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
	h.recordAdminAudit(r, audit.Entry{
		Action:       "token.revoke",
		ResourceType: "api_token",
		ResourceID:   id.String(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "revoked"})
}

// handleMemories returns a bounded, deterministic page with filter-aware totals.
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
	page, pageSize, err := parseMemoryPage(q)
	if err != nil {
		writeJSONError(w, err)
		return
	}

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
	if projectIDStr := strings.TrimSpace(q.Get("project_id")); projectIDStr != "" {
		pid, err := uuid.Parse(projectIDStr)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid project_id"))
			return
		}
		where = append(where, "m.project_id = "+addArg(pid))
	}
	if category := strings.TrimSpace(q.Get("category")); category != "" {
		where = append(where, "m.category = "+addArg(category))
	}
	if tag := strings.TrimSpace(q.Get("tag")); tag != "" {
		where = append(where, addArg(tag)+" = ANY(m.tags)")
	}
	if memoryID := q.Get("memory_id"); memoryID != "" {
		id, err := uuid.Parse(memoryID)
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id"))
			return
		}
		where = append(where, "m.id = "+addArg(id))
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := h.pool.QueryRow(r.Context(), `SELECT count(*) FROM memories m `+whereClause, args...).Scan(&total); err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "count memories", err))
		return
	}
	// Clamp stale pages after a deletion or a restore changes the filtered result set.
	lastPage := max(1, (total+pageSize-1)/pageSize)
	page = min(page, lastPage)
	// Pinned entries lead the knowledge list; sort=recent keeps a pure recency order.
	orderBy := "m.is_pinned DESC, m.updated_at DESC, m.id DESC"
	if q.Get("sort") == "recent" {
		orderBy = "m.updated_at DESC, m.id DESC"
	}
	limitArg := addArg(pageSize)
	offsetArg := addArg((page - 1) * pageSize)
	rows, err := h.pool.Query(r.Context(), `
		SELECT m.id, m.tenant_id, m.scope, m.project_id, coalesce(p.name, ''),
		       m.category, m.title, m.content_md, m.tags, m.is_pinned, m.status,
		       m.version, m.updated_at, m.deleted_at, m.created_agent, m.updated_agent
		FROM memories m
		LEFT JOIN projects p ON p.id = m.project_id
		`+whereClause+`
		ORDER BY `+orderBy+`
		LIMIT `+limitArg+` OFFSET `+offsetArg+`
	`, args...)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list memories", err))
		return
	}
	defer rows.Close()

	items := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, tid               uuid.UUID
			projID                *uuid.UUID
			sc, pName, cat, title string
			content, status       string
			tags                  []string
			isPinned              bool
			version               int64
			updatedAt             time.Time
			deletedAt             *time.Time
		)
		var createdAgent, updatedAgent string
		if err := rows.Scan(&id, &tid, &sc, &projID, &pName, &cat, &title, &content, &tags, &isPinned, &status, &version, &updatedAt, &deletedAt, &createdAgent, &updatedAgent); err != nil {
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

			"created_agent": createdAgent,
			"updated_agent": updatedAgent,
		})
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "iterate memories", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// parseMemoryPage validates pagination before database access and bounds offset arithmetic.
func parseMemoryPage(q url.Values) (int, int, error) {
	const defaultPageSize = 20
	const maxPageSize = 100
	const maxPage = 1000000
	page, size := 1, defaultPageSize
	for _, param := range []struct {
		key string
		dst *int
		max int
	}{{"page", &page, maxPage}, {"page_size", &size, maxPageSize}} {
		if raw := q.Get(param.key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > param.max {
				return 0, 0, apperror.New(apperror.CodeInvalidArgument, "invalid "+param.key)
			}
			*param.dst = value
		}
	}
	return page, size, nil
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
	h.recordAdminAudit(r, audit.Entry{
		TenantID:     &tid,
		Action:       "memory.restore",
		ResourceType: "memory",
		ResourceID:   memID.String(),
		Details:      map[string]any{"expected_version": req.ExpectedVersion},
	})
	writeJSON(w, http.StatusOK, restored)
}

// memoryActionRequest identifies a memory and the version the console last displayed.
type memoryActionRequest struct {
	TenantID        string `json:"tenant_id"`
	MemoryID        string `json:"memory_id"`
	ExpectedVersion int64  `json:"expected_version"`
	IsPinned        bool   `json:"is_pinned"`
}

// decodeMemoryAction parses a console memory action and resolves the acting tenant administrator.
func (h *Handler) decodeMemoryAction(w http.ResponseWriter, r *http.Request) (req memoryActionRequest, tid, memID, actorID uuid.UUID, ok bool) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	var err error
	if tid, err = uuid.Parse(req.TenantID); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
		return
	}
	if memID, err = uuid.Parse(req.MemoryID); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid memory_id"))
		return
	}
	if actorID, err = h.ensureAdminActorForTenant(r.Context(), tid); err != nil {
		writeJSONError(w, err)
		return
	}
	return req, tid, memID, actorID, true
}

// handleDeleteMemory soft-deletes a memory; it stays restorable from the recycle bin.
func (h *Handler) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	req, tid, memID, actorID, ok := h.decodeMemoryAction(w, r)
	if !ok {
		return
	}
	deleted, err := h.memSvc.AdminDelete(r.Context(), memory.AdminDeleteRequest{
		TenantID:        tid,
		ActorID:         actorID,
		MemoryID:        memID,
		ExpectedVersion: req.ExpectedVersion,
		RequestID:       "webui-delete-" + uuid.NewString(),
	})
	if err != nil {
		writeJSONError(w, err)
		return
	}
	h.recordAdminAudit(r, audit.Entry{
		TenantID:     &tid,
		Action:       "memory.delete",
		ResourceType: "memory",
		ResourceID:   memID.String(),
		Details:      map[string]any{"expected_version": req.ExpectedVersion},
	})
	writeJSON(w, http.StatusOK, deleted)
}

// handlePinMemory sets or clears the pinned flag that keeps a memory at the top of indexes.
func (h *Handler) handlePinMemory(w http.ResponseWriter, r *http.Request) {
	req, tid, memID, actorID, ok := h.decodeMemoryAction(w, r)
	if !ok {
		return
	}
	updated, err := h.memSvc.AdminSetPinned(r.Context(), memory.AdminPinRequest{
		TenantID:        tid,
		ActorID:         actorID,
		MemoryID:        memID,
		ExpectedVersion: req.ExpectedVersion,
		IsPinned:        req.IsPinned,
		RequestID:       "webui-pin-" + uuid.NewString(),
	})
	if err != nil {
		writeJSONError(w, err)
		return
	}
	action := "memory.unpin"
	if req.IsPinned {
		action = "memory.pin"
	}
	h.recordAdminAudit(r, audit.Entry{
		TenantID:     &tid,
		Action:       action,
		ResourceType: "memory",
		ResourceID:   memID.String(),
		Details:      map[string]any{"expected_version": req.ExpectedVersion, "is_pinned": req.IsPinned},
	})
	writeJSON(w, http.StatusOK, updated)
}

// handleMemoryFacets lists the categories and most used tags of active memories for list filters.
func (h *Handler) handleMemoryFacets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	type facet struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	collect := func(query string) ([]facet, error) {
		rows, err := h.pool.Query(r.Context(), query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := make([]facet, 0)
		for rows.Next() {
			var f facet
			if err := rows.Scan(&f.Value, &f.Count); err != nil {
				return nil, err
			}
			items = append(items, f)
		}
		return items, rows.Err()
	}
	categories, err := collect(`SELECT category, count(*)::int FROM memories WHERE deleted_at IS NULL GROUP BY category ORDER BY count(*) DESC, category`)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list memory categories", err))
		return
	}
	tags, err := collect(`SELECT tag, count(*)::int FROM memories, unnest(tags) AS tag WHERE deleted_at IS NULL GROUP BY tag ORDER BY count(*) DESC, tag LIMIT 200`)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "list memory tags", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": categories, "tags": tags})
}

// purgeTokenTTL bounds how long a downloaded backup can authorize the matching purge.
const purgeTokenTTL = 30 * time.Minute

// purgeScope is the set of memories a bulk purge targets: a tenant, optionally narrowed to its
// global scope or to one project.
type purgeScope struct {
	TenantID  uuid.UUID  `json:"tenant_id"`
	Scope     string     `json:"scope,omitempty"`
	ProjectID *uuid.UUID `json:"project_id,omitempty"`
	MemoryID  *uuid.UUID `json:"memory_id,omitempty"`
	Mode      string     `json:"mode"`
}

// purgeArchiveDeleted maps a purge mode to the recycle-bin selection of its backup archive.
var purgeArchiveDeleted = map[string]string{memory.PurgeSoft: "", memory.PurgeHard: "all", memory.PurgeTrash: "only"}

// purgeTicket travels to the browser sealed with the server key; it binds a purge to the exact
// memory versions contained in the backup the browser saved.
type purgeTicket struct {
	purgeScope
	Digest    string    `json:"digest"`
	Count     int       `json:"count"`
	ExpiresAt time.Time `json:"expires_at"`
}

// resolvePurgeScope validates the requested scope and returns display names for the backup manifest.
func (h *Handler) resolvePurgeScope(ctx context.Context, tenantIDStr, scope, projectIDStr, memoryIDStr, mode string) (purgeScope, string, string, error) {
	var ps purgeScope
	ps.Mode = strings.TrimSpace(mode)
	if ps.Mode == "" {
		ps.Mode = memory.PurgeSoft
	}
	if _, ok := purgeArchiveDeleted[ps.Mode]; !ok {
		return ps, "", "", apperror.New(apperror.CodeInvalidArgument, "invalid purge mode")
	}
	tid, err := uuid.Parse(strings.TrimSpace(tenantIDStr))
	if err != nil {
		return ps, "", "", apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id")
	}
	ps.TenantID = tid
	var tenantName string
	if err := h.pool.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`, tid).Scan(&tenantName); err != nil {
		return ps, "", "", apperror.New(apperror.CodeInvalidArgument, "tenant not found")
	}
	var projectName string
	switch strings.TrimSpace(scope) {
	case "":
	case "global":
		ps.Scope = "global"
	case "project":
		ps.Scope = "project"
		pid, err := uuid.Parse(strings.TrimSpace(projectIDStr))
		if err != nil {
			return ps, "", "", apperror.New(apperror.CodeInvalidArgument, "invalid project_id")
		}
		if err := h.pool.QueryRow(ctx, `SELECT name FROM projects WHERE id = $1 AND tenant_id = $2`, pid, tid).Scan(&projectName); err != nil {
			return ps, "", "", apperror.New(apperror.CodeInvalidArgument, "project not found")
		}
		ps.ProjectID = &pid
	default:
		return ps, "", "", apperror.New(apperror.CodeInvalidArgument, "invalid scope")
	}
	if strings.TrimSpace(memoryIDStr) != "" {
		mid, err := uuid.Parse(strings.TrimSpace(memoryIDStr))
		if err != nil {
			return ps, "", "", apperror.New(apperror.CodeInvalidArgument, "invalid memory_id")
		}
		var exists bool
		if err := h.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memories WHERE id = $1 AND tenant_id = $2)`, mid, tid).Scan(&exists); err != nil || !exists {
			return ps, "", "", apperror.New(apperror.CodeMemoryNotFound, "memory not found")
		}
		ps.MemoryID = &mid
	}
	return ps, tenantName, projectName, nil
}

// handlePurgeBackup returns a Markdown archive of every active memory in the requested scope,
// together with a sealed ticket that authorizes purging exactly those memory versions.
func (h *Handler) handlePurgeBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		TenantID  string `json:"tenant_id"`
		Scope     string `json:"scope"`
		ProjectID string `json:"project_id"`
		MemoryID  string `json:"memory_id"`
		Mode      string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	ps, tenantName, projectName, err := h.resolvePurgeScope(r.Context(), req.TenantID, req.Scope, req.ProjectID, req.MemoryID, req.Mode)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	manifest := map[string]any{
		"purpose":     "purge-backup",
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"tenant_id":   ps.TenantID,
		"tenant_name": tenantName,
		"scope":       ps.Scope,
		"mode":        ps.Mode,
	}
	if ps.MemoryID != nil {
		manifest["memory_id"] = ps.MemoryID
	}
	if ps.ProjectID != nil {
		manifest["project_id"] = ps.ProjectID
		manifest["project_name"] = projectName
	}
	var buf bytes.Buffer
	archived, err := h.exporter.ExportFilteredArchive(r.Context(), &buf, exporter.ArchiveFilter{TenantID: &ps.TenantID, Scope: ps.Scope, ProjectID: ps.ProjectID, MemoryID: ps.MemoryID, Deleted: purgeArchiveDeleted[ps.Mode]}, manifest)
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "export purge backup", err))
		return
	}
	versions := make([]memory.MemoryVersion, 0, len(archived))
	for _, item := range archived {
		versions = append(versions, memory.MemoryVersion{ID: item.ID, Version: item.Version})
	}
	ticket, err := json.Marshal(purgeTicket{purgeScope: ps, Digest: memory.PurgeDigest(versions), Count: len(versions), ExpiresAt: time.Now().UTC().Add(purgeTokenTTL)})
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "encode purge ticket", err))
		return
	}
	sealed, err := h.authMgr.SealToken(string(ticket))
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "seal purge ticket", err))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="engram-purge-backup.zip"`)
	w.Header().Set("X-Purge-Token", sealed)
	w.Header().Set("X-Purge-Count", strconv.Itoa(len(versions)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// handlePurge soft-deletes the memories captured by a backup ticket; it refuses when the scope
// changed after the backup, so nothing is removed without being in the saved archive.
func (h *Handler) handlePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PurgeToken string `json:"purge_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PurgeToken) == "" {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "purge_token is required"))
		return
	}
	raw, err := h.authMgr.UnsealToken(req.PurgeToken)
	var ticket purgeTicket
	if err != nil || json.Unmarshal([]byte(raw), &ticket) != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid purge_token"))
		return
	}
	if time.Now().UTC().After(ticket.ExpiresAt) {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "PURGE_TOKEN_EXPIRED", "message": "备份已超过 30 分钟，请重新下载备份后再清空。"})
		return
	}
	actorID, err := h.ensureAdminActorForTenant(r.Context(), ticket.TenantID)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	purged, err := h.memSvc.AdminPurge(r.Context(), memory.AdminPurgeRequest{
		TenantID:  ticket.TenantID,
		ActorID:   actorID,
		Scope:     ticket.Scope,
		ProjectID: ticket.ProjectID,
		MemoryID:  ticket.MemoryID,
		Mode:      ticket.Mode,
		Digest:    ticket.Digest,
		RequestID: "webui-purge-" + uuid.NewString(),
	})
	if err != nil {
		if appErr := apperror.AsError(err, ""); appErr.Code == apperror.CodeVersionConflict {
			writeJSON(w, http.StatusConflict, map[string]any{"code": "PURGE_SCOPE_CHANGED", "message": "备份下载后这些知识又有变化，为避免误删未备份的内容，本次没有清空。请重新下载备份后再试。"})
			return
		}
		writeJSONError(w, err)
		return
	}
	h.recordAdminAudit(r, audit.Entry{
		TenantID:     &ticket.TenantID,
		ProjectID:    ticket.ProjectID,
		Action:       "memory.purge",
		ResourceType: "memory",
		Details: map[string]any{
			"scope":  ticket.Scope,
			"mode":   ticket.Mode,
			"purged": purged,
		},
	})
	writeJSON(w, http.StatusOK, map[string]any{"purged": purged})
}

// ensureAdminActorForTenant creates a console-only audit identity without any member grants.
func (h *Handler) ensureAdminActorForTenant(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "begin console actor transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	actorID, err := ensureAdminActorForTenantTx(ctx, tx, tenantID)
	if err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, apperror.Wrap(apperror.CodeInternalError, "commit console actor transaction", err)
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
	h.recordAdminAudit(r, audit.Entry{
		Action:       "export.markdown",
		ResourceType: "export",
		Details:      map[string]any{"exported_count": count},
	})
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
	h.recordAdminAudit(r, audit.Entry{
		Action:       "export.archive",
		ResourceType: "export",
		Details:      map[string]any{"exported_count": count},
	})
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
