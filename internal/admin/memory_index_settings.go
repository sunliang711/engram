package admin

import (
	"io"
	"net/http"

	"engram/internal/apperror"
	"engram/internal/audit"
	"engram/internal/memory"
)

// handleMemoryIndexSettings reads or updates persisted discovery index limits.
func (h *Handler) handleMemoryIndexSettings(w http.ResponseWriter, r *http.Request) {
	if h.memSvc == nil {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, memory.DefaultIndexSettings())
			return
		}
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "memory service not initialized"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		settings, err := h.memSvc.GetIndexSettings(r.Context())
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)

	case http.MethodPut, http.MethodPatch, http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			writeJSONError(w, apperror.Wrap(apperror.CodeInvalidArgument, "read request body", err))
			return
		}
		var req memory.UpdateIndexSettingsRequest
		if err := memory.DecodeStrictJSON(body, &req); err != nil {
			writeJSONError(w, err)
			return
		}
		updated, err := h.memSvc.UpdateIndexSettings(r.Context(), req)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		h.recordAdminAudit(r, audit.Entry{
			Action:       "settings.memory_index_update",
			ResourceType: "settings",
			ResourceID:   "memory_index",
			Details: map[string]any{
				"discover_pinned_limit":         updated.DiscoverPinnedLimit,
				"discover_global_recent_limit":  updated.DiscoverGlobalRecentLimit,
				"discover_project_recent_limit": updated.DiscoverProjectRecentLimit,
				"index_summary_max_runes":       updated.IndexSummaryMaxRunes,
			},
		})
		writeJSON(w, http.StatusOK, updated)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
