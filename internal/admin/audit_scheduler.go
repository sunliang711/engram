package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"engram/internal/apperror"
	"engram/internal/audit"
	"engram/internal/config"
	"engram/internal/scheduler"

	"github.com/google/uuid"
)

func parseAuditQueryFilter(q url.Values) (audit.QueryFilter, error) {
	page, size, err := parseMemoryPage(q)
	if err != nil {
		return audit.QueryFilter{}, err
	}
	f := audit.QueryFilter{
		Source:       strings.TrimSpace(q.Get("source")),
		Action:       strings.TrimSpace(q.Get("action")),
		Status:       strings.TrimSpace(q.Get("status")),
		ResourceType: strings.TrimSpace(q.Get("resource_type")),
		Query:        strings.TrimSpace(q.Get("q")),
		Page:         page,
		PageSize:     size,
	}
	if rawTID := strings.TrimSpace(q.Get("tenant_id")); rawTID != "" {
		tid, err := uuid.Parse(rawTID)
		if err != nil {
			return audit.QueryFilter{}, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id")
		}
		f.TenantID = &tid
	}
	if rawFrom := strings.TrimSpace(q.Get("from")); rawFrom != "" {
		t, err := parseAuditTime(rawFrom, false)
		if err != nil {
			return audit.QueryFilter{}, apperror.New(apperror.CodeInvalidArgument, "invalid from timestamp")
		}
		f.From = &t
	}
	if rawTo := strings.TrimSpace(q.Get("to")); rawTo != "" {
		t, err := parseAuditTime(rawTo, true)
		if err != nil {
			return audit.QueryFilter{}, apperror.New(apperror.CodeInvalidArgument, "invalid to timestamp")
		}
		f.To = &t
	}
	return f, nil
}

func parseAuditTime(raw string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if endOfDay {
			return t.UTC().Add(24*time.Hour - time.Nanosecond), nil
		}
		return t.UTC(), nil
	}
	return time.Time{}, apperror.New(apperror.CodeInvalidArgument, "invalid time format")
}

func (h *Handler) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auditSvc == nil {
		writeJSON(w, http.StatusOK, audit.QueryResult{Items: []audit.Entry{}, Total: 0, Page: 1, PageSize: 20})
		return
	}
	f, err := parseAuditQueryFilter(r.URL.Query())
	if err != nil {
		writeJSONError(w, err)
		return
	}
	res, err := h.auditSvc.QueryLogs(r.Context(), f)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) handleAuditLogsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auditSvc == nil {
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "audit service not initialized"))
		return
	}
	f, err := parseAuditQueryFilter(r.URL.Query())
	if err != nil {
		writeJSONError(w, err)
		return
	}
	items, err := h.auditSvc.ExportLogs(r.Context(), f, 5000)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	h.recordAdminAudit(r, audit.Entry{
		Action:       "audit.export",
		ResourceType: "audit_log",
		Details:      map[string]any{"exported_count": len(items)},
	})
	payload, err := json.MarshalIndent(map[string]any{
		"exported_at": time.Now().UTC(),
		"count":       len(items),
		"items":       items,
	}, "", "  ")
	if err != nil {
		writeJSONError(w, apperror.Wrap(apperror.CodeInternalError, "encode audit export", err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="engram-audit-logs.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(payload, '\n'))
}

func (h *Handler) handleAuditStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auditSvc == nil {
		writeJSON(w, http.StatusOK, audit.Stats{BySource: map[string]int64{}, ByStatus: map[string]int64{}})
		return
	}
	stats, err := h.auditSvc.GetStats(r.Context())
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleAuditCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auditSvc == nil {
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "audit service not initialized"))
		return
	}
	var req struct {
		Retention string `json:"retention"`
		BatchSize int    `json:"batch_size"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
	}

	retention := h.auditSvc.DefaultRetention()
	retLabel := strings.TrimSpace(req.Retention)
	if retLabel == "" && h.scheduler != nil {
		if task, err := h.scheduler.GetTask(r.Context(), scheduler.TaskAuditLogCleanup); err == nil {
			if raw, ok := task.Config["retention"].(string); ok && strings.TrimSpace(raw) != "" {
				retLabel = strings.TrimSpace(raw)
			}
		}
	}
	if retLabel != "" {
		parsed, err := config.ParseDuration(retLabel)
		if err != nil || parsed < time.Minute {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "retention must be a valid duration >= 1m (e.g. \"30d\", \"720h\")"))
			return
		}
		retention = parsed
	} else {
		retLabel = retention.String()
	}

	deleted, cutoff, err := h.auditSvc.CleanupExpired(r.Context(), retention, req.BatchSize)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	h.recordAdminAudit(r, audit.Entry{
		Action:       "audit.cleanup",
		ResourceType: "audit_log",
		Details: map[string]any{
			"retention":     retLabel,
			"cutoff":        cutoff.Format(time.RFC3339),
			"deleted_count": deleted,
		},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted_count": deleted,
		"retention":     retLabel,
		"cutoff":        cutoff,
	})
}

func (h *Handler) handleAuditTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auditSvc == nil {
		writeJSON(w, http.StatusOK, map[string]any{"definitions": []audit.TaskDefinition{}, "runs": []audit.TaskRun{}, "total": 0, "page": 1, "page_size": 20})
		return
	}
	page, size, err := parseMemoryPage(r.URL.Query())
	if err != nil {
		writeJSONError(w, err)
		return
	}
	taskType := strings.TrimSpace(r.URL.Query().Get("task_type"))
	runs, total, err := h.auditSvc.ListTaskRuns(r.Context(), taskType, page, size)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"definitions": h.auditSvc.ListTaskDefinitions(),
		"runs":        runs,
		"total":       total,
		"page":        page,
		"page_size":   size,
	})
}

func (h *Handler) handleAuditTaskRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.auditSvc == nil {
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "audit service not initialized"))
		return
	}
	var req struct {
		TaskType string `json:"task_type"`
		TenantID string `json:"tenant_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	var tidPtr *uuid.UUID
	if strings.TrimSpace(req.TenantID) != "" {
		tid, err := uuid.Parse(strings.TrimSpace(req.TenantID))
		if err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid tenant_id"))
			return
		}
		tidPtr = &tid
	}
	actorName := adminActorFromRequest(r)
	run, err := h.auditSvc.RunTask(r.Context(), req.TaskType, tidPtr, actorName, "manual")
	if err != nil {
		writeJSONError(w, err)
		return
	}
	h.recordAdminAudit(r, audit.Entry{
		TenantID:     tidPtr,
		Action:       "audit.task_run",
		ResourceType: "audit_task",
		ResourceID:   req.TaskType,
		DurationMS:   run.DurationMS,
		Details: map[string]any{
			"run_id":         run.ID.String(),
			"findings_count": run.FindingsCount,
			"summary":        run.Summary,
		},
	})
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) handleSchedulerTasks(w http.ResponseWriter, r *http.Request) {
	if h.scheduler == nil {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"items": []scheduler.TaskView{}})
			return
		}
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "scheduler not initialized"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := h.scheduler.ListTasks(r.Context())
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})

	case http.MethodPatch:
		var req scheduler.UpdateTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
		updated, err := h.scheduler.UpdateTask(r.Context(), req, adminActorFromRequest(r))
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSchedulerPreview validates unsaved plans and returns five actual future occurrences.
func (h *Handler) handleSchedulerPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req scheduler.SchedulePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
		return
	}
	times, err := scheduler.PreviewSchedule(req, time.Now().UTC())
	if err != nil {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"next_runs": times})
}

func (h *Handler) handleSchedulerTaskRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.scheduler == nil {
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "scheduler not initialized"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "task name is required"))
		return
	}
	run, err := h.scheduler.TriggerTask(r.Context(), req.Name, adminActorFromRequest(r))
	if err != nil {
		writeJSONError(w, err)
		return
	}
	task, _ := h.scheduler.GetTask(r.Context(), req.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"task": task,
		"run":  run,
	})
}

func (h *Handler) handleSchedulerRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.scheduler == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []scheduler.TaskRun{}, "total": 0, "page": 1, "page_size": 20})
		return
	}
	page, size, err := parseMemoryPage(r.URL.Query())
	if err != nil {
		writeJSONError(w, err)
		return
	}
	taskName := strings.TrimSpace(r.URL.Query().Get("task_name"))
	items, total, err := h.scheduler.ListRuns(r.Context(), taskName, page, size)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": size,
	})
}
