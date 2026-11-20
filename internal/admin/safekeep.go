package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"engram/internal/apperror"
	"engram/internal/audit"
	"engram/internal/safekeep"
	"engram/internal/scheduler"

	"github.com/google/uuid"
)

// ExportMigrationArchive generates an encrypted or plain migration ZIP archive for automated SafeKeep backups.
func (h *Handler) ExportMigrationArchive(ctx context.Context, req scheduler.MigrationArchiveRequest) (*scheduler.MigrationArchiveResult, error) {
	if h == nil || h.pool == nil {
		return nil, apperror.New(apperror.CodeInternalError, "admin handler not initialized")
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = MigrationModeSystem
	}

	now := time.Now().UTC()
	switch mode {
	case MigrationModeSystem:
		payload, err := h.exportSnapshotPayload(ctx, req.Passphrase != "")
		if err != nil {
			return nil, err
		}
		zipBytes, manifest, err := EncodeSnapshotArchive(payload, req.Passphrase, now)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "encode migration snapshot", err)
		}
		return &scheduler.MigrationArchiveResult{
			Filename:  fmt.Sprintf("engram-system-snapshot-%s.zip", manifest.ExportedAt.Format("20060102T150405Z")),
			Data:      zipBytes,
			Format:    manifest.Format,
			Encrypted: manifest.Encrypted,
			Tenants:   manifest.Counts.Tenants,
			Projects:  manifest.Counts.Projects,
			Memories:  manifest.Counts.Memories,
		}, nil

	case MigrationModeKnowledge:
		bundle, err := h.exportKnowledgeBundlePayload(ctx, KnowledgeExportOptions{
			IncludeDeleted: req.IncludeDeleted,
		})
		if err != nil {
			return nil, err
		}
		zipBytes, manifest, err := EncodeKnowledgeArchive(bundle, req.Passphrase, now)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "encode knowledge bundle", err)
		}
		return &scheduler.MigrationArchiveResult{
			Filename:  fmt.Sprintf("engram-knowledge-bundle-%s.zip", manifest.ExportedAt.Format("20060102T150405Z")),
			Data:      zipBytes,
			Format:    manifest.Format,
			Encrypted: manifest.Encrypted,
			Tenants:   manifest.Counts.Tenants,
			Projects:  manifest.Counts.Projects,
			Memories:  manifest.Counts.Memories,
		}, nil

	default:
		return nil, apperror.New(apperror.CodeInvalidArgument, "unsupported migration mode: "+mode)
	}
}

func (h *Handler) handleSafekeepSettings(w http.ResponseWriter, r *http.Request) {
	if h.safekeepSvc == nil {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, safekeep.SettingsView{DefaultMode: "sync", DefaultPath: "engram"})
			return
		}
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "safekeep service not initialized"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		view, err := h.safekeepSvc.GetSettings(r.Context())
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)

	case http.MethodPut, http.MethodPatch, http.MethodPost:
		var req safekeep.UpdateSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
		view, err := h.safekeepSvc.UpdateSettings(r.Context(), req)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		h.recordAdminAudit(r, audit.Entry{
			Action:       "integration.safekeep_update",
			ResourceType: "integration",
			ResourceID:   "safekeep",
			Details: map[string]any{
				"enabled":         view.Enabled,
				"base_url":        view.BaseURL,
				"has_api_key":     view.HasAPIKey,
				"default_mode":    view.DefaultMode,
				"default_path":    view.DefaultPath,
				"default_targets": view.DefaultTargets,
			},
		})
		writeJSON(w, http.StatusOK, view)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleSafekeepTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.safekeepSvc == nil {
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "safekeep service not initialized"))
		return
	}
	var req safekeep.TestRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
	}
	res, err := h.safekeepSvc.TestConnection(r.Context(), req)
	status := audit.StatusSuccess
	errMsg := ""
	if err != nil {
		status = audit.StatusFailed
		errMsg = err.Error()
	}
	h.recordAdminAudit(r, audit.Entry{
		Action:       "integration.safekeep_test",
		ResourceType: "integration",
		ResourceID:   "safekeep",
		Status:       status,
		ErrorMessage: errMsg,
		Details: map[string]any{
			"ok":         res != nil && res.OK,
			"latency_ms": resLatency(res),
		},
	})
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func resLatency(res *safekeep.TestResult) int64 {
	if res == nil {
		return 0
	}
	return res.LatencyMS
}

func (h *Handler) handleSafekeepBackupNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.safekeepSvc == nil {
		writeJSONError(w, apperror.New(apperror.CodeInternalError, "safekeep service not initialized"))
		return
	}

	var req struct {
		Mode           string `json:"mode"`
		Passphrase     string `json:"passphrase"`
		IncludeDeleted *bool  `json:"include_deleted"`
		UploadMode     string `json:"upload_mode"`
		Path           string `json:"path"`
		Targets        string `json:"targets"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, apperror.New(apperror.CodeInvalidArgument, "invalid json body"))
			return
		}
	}

	includeDeleted := true
	if req.IncludeDeleted != nil {
		includeDeleted = *req.IncludeDeleted
	}
	passphrase := req.Passphrase
	if passphrase == "" {
		storedPass, err := h.safekeepSvc.BackupPassphrase(r.Context())
		if err != nil {
			writeJSONError(w, err)
			return
		}
		passphrase = storedPass
	}

	archive, err := h.ExportMigrationArchive(r.Context(), scheduler.MigrationArchiveRequest{
		Mode:           req.Mode,
		IncludeDeleted: includeDeleted,
		Passphrase:     passphrase,
	})
	if err != nil {
		writeJSONError(w, err)
		return
	}

	uploadRes, err := h.safekeepSvc.UploadArchive(r.Context(), archive.Filename, archive.Data, safekeep.UploadOptions{
		Mode:           req.UploadMode,
		Path:           req.Path,
		Targets:        req.Targets,
		IdempotencyKey: "engram-manual-" + uuid.NewString(),
		Metadata: map[string]any{
			"source":         "engram",
			"migration_mode": strings.TrimSpace(req.Mode),
			"format":         archive.Format,
			"encrypted":      archive.Encrypted,
			"tenants":        archive.Tenants,
			"projects":       archive.Projects,
			"memories":       archive.Memories,
		},
	})
	if err != nil {
		appErr := apperror.AsError(err, "")
		h.recordAdminAudit(r, audit.Entry{
			Action:       "integration.safekeep_backup",
			ResourceType: "integration",
			ResourceID:   "safekeep",
			Status:       audit.StatusFailed,
			ErrorCode:    string(appErr.Code),
			ErrorMessage: appErr.Message,
			Details:      map[string]any{"filename": archive.Filename},
		})
		writeJSONError(w, err)
		return
	}

	h.recordAdminAudit(r, audit.Entry{
		Action:       "integration.safekeep_backup",
		ResourceType: "integration",
		ResourceID:   "safekeep",
		Details: map[string]any{
			"filename":        archive.Filename,
			"file_size":       len(archive.Data),
			"encrypted":       archive.Encrypted,
			"memories":        archive.Memories,
			"safekeep_task":   uploadRes.TaskID,
			"safekeep_status": uploadRes.Status,
		},
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"filename":  archive.Filename,
		"file_size": len(archive.Data),
		"format":    archive.Format,
		"encrypted": archive.Encrypted,
		"memories":  archive.Memories,
		"safekeep":  uploadRes,
	})
}
