package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"engram/internal/apperror"
	"engram/internal/audit"

	"github.com/google/uuid"
)

func sessionMetadata(r *http.Request) SessionMetadata {
	return SessionMetadata{ClientIP: requestClientIP(r), UserAgent: r.UserAgent()}
}

func canAccessConsole(role, method, path string) bool {
	if role == "admin" {
		return true
	}
	if role != "viewer" {
		return false
	}
	// 只读账号只能撤销自己的会话；密钥明文、迁移与导出仅限管理员。
	if path == "/api/admin/sessions" || strings.HasPrefix(path, "/api/admin/sessions/") {
		return method == http.MethodGet || method == http.MethodDelete
	}
	if path == "/api/admin/accounts" || strings.HasPrefix(path, "/api/admin/accounts/") ||
		strings.HasPrefix(path, "/api/admin/export") || strings.HasPrefix(path, "/api/admin/migration/") {
		return false
	}
	return method == http.MethodGet
}

func (h *Handler) handleSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := r.Context().Value(adminStateCtxKey{}).(AdminState)
	if !ok {
		writeJSONError(w, apperror.New(apperror.CodeUnauthenticated, "admin authentication required"))
		return
	}
	if r.URL.Path == "/api/admin/sessions" && r.Method == http.MethodGet {
		items, err := h.authMgr.ListSessions(r.Context(), actor)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
		return
	}
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/sessions/")
	parsedID, err := uuid.Parse(id)
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid session id"))
		return
	}
	id = parsedID.String()
	if err := h.authMgr.RevokeSession(r.Context(), actor, id); err != nil {
		writeJSONError(w, err)
		return
	}
	if id == actor.SessionID {
		h.clearSessionCookie(w, r)
	}
	h.recordAdminAudit(r, audit.Entry{Action: "admin.session_revoke", ResourceType: "admin_session", ResourceID: id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleAccounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := r.Context().Value(adminStateCtxKey{}).(AdminState)
	if !ok || actor.Role != "admin" {
		writeJSONError(w, apperror.New(apperror.CodeUnauthorized, "administrator access required"))
		return
	}
	if r.URL.Path == "/api/admin/accounts" && r.Method == http.MethodGet {
		items, err := h.authMgr.ListAccounts(r.Context())
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accounts": items})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, action := "", "create"
	if r.URL.Path != "/api/admin/accounts" {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/admin/accounts/"), "/")
		if len(parts) != 2 {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid console account action"))
			return
		}
		id, action = parts[0], parts[1]
		if _, err := uuid.Parse(id); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid console account id"))
			return
		}
		if action != "role" && action != "enable" && action != "disable" && action != "reset-password" {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid console account action"))
			return
		}
	}
	var input accountChange
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	if err := h.loginLimiter.AllowKey("account-change:" + actor.Username); err != nil {
		writeJSONError(w, err)
		return
	}
	changedID, err := h.authMgr.ChangeAccount(r.Context(), actor, id, action, input)
	entry := audit.Entry{Action: "admin.account_" + strings.ReplaceAll(action, "-", "_"), ResourceType: "admin_account", ResourceID: id}
	if err != nil {
		appErr := apperror.AsError(err, "")
		entry.Status, entry.ErrorCode, entry.ErrorMessage = audit.StatusFailed, string(appErr.Code), appErr.Message
		h.recordAdminAudit(r, entry)
		writeJSONError(w, err)
		return
	}
	entry.ResourceID = changedID
	h.recordAdminAudit(r, entry)
	status := http.StatusOK
	if action == "create" {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"id": changedID})
}
