package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"engram/internal/audit"
	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"
	"engram/internal/safekeep"
	"engram/internal/scheduler"

	"github.com/google/uuid"
)

func TestAdminSafekeepSettingsAndScheduledMigrationBackup(t *testing.T) {
	dsn := os.Getenv("ENGRAM_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_ADMIN_TEST_DSN is required for admin safekeep PostgreSQL tests")
	}

	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	cfg.Database.AutoMigrate = true
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var uploadedFilename string
	var uploadedZip []byte
	mockSafekeep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-API-Key") != "sk_integration_key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid api key"}}`))
			return
		}
		switch r.URL.Path {
		case "/api/v1/upstreams":
			_, _ = w.Write([]byte(`{"items":[{"id":"up1","name":"local-s3","provider":"s3","enabled":true}]}`))
		case "/api/v1/backups":
			if err := r.ParseMultipartForm(16 << 20); err != nil {
				t.Fatal(err)
			}
			f, hdr, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			uploadedFilename = hdr.Filename
			uploadedZip, _ = io.ReadAll(f)
			_, _ = w.Write([]byte(`{"task":{"id":"sk-task-01","status":"completed","original_filename":"` + hdr.Filename + `","file_size":` + "100" + `,"sha256":"deadbeef","mode":"sync"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockSafekeep.Close()

	log := observability.NewLogger("error", nil)
	metrics := observability.NewMetrics()
	authMgr := NewAuthManager(database.Pool, cfg.Server.ContextSignKey, cfg.Server.ContextTTL, false, false)
	auditSvc := audit.NewService(database.Pool, cfg.Audit, metrics, log)
	skSvc := safekeep.NewService(database.Pool, authMgr, log)
	h := NewHandler(database.Pool, authMgr, nil, nil, log)
	h.SetAuditService(auditSvc)
	h.SetSafekeepService(skSvc)

	sched := scheduler.New(database.Pool, cfg.Scheduler, auditSvc, metrics, log)
	if err := sched.RegisterBuiltinTasks(cfg.Audit); err != nil {
		t.Fatal(err)
	}
	if err := sched.RegisterSafekeepBackupTask(skSvc, h.ExportMigrationArchive); err != nil {
		t.Fatal(err)
	}
	if err := sched.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	h.SetScheduler(sched)

	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM safekeep_settings`)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE action LIKE 'integration.%' OR action LIKE 'scheduler.%'`)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM scheduled_task_runs WHERE task_name = $1`, scheduler.TaskSafekeepMigrationBackup)
		_, _ = database.Pool.Exec(ctx, `UPDATE scheduled_tasks SET enabled = false WHERE name = $1`, scheduler.TaskSafekeepMigrationBackup)
	})

	// 1. Save SafeKeep URL, API Key, and optional backup passphrase via HTTP
	savePayload, _ := json.Marshal(map[string]any{
		"enabled":           true,
		"base_url":          mockSafekeep.URL,
		"api_key":           "sk_integration_key",
		"backup_passphrase": "backup-secret-passphrase",
		"default_mode":      "sync",
		"default_path":      "engram/test",
	})
	rec := httptest.NewRecorder()
	h.handleSafekeepSettings(rec, httptest.NewRequest(http.MethodPut, "/api/admin/integrations/safekeep", bytes.NewReader(savePayload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save safekeep settings: %d %s", rec.Code, rec.Body.String())
	}
	var view safekeep.SettingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || !view.HasAPIKey || !view.HasBackupPassphrase {
		t.Fatalf("unexpected saved settings view: %+v", view)
	}

	// 2. Test connectivity via saved credentials
	rec = httptest.NewRecorder()
	h.handleSafekeepTest(rec, httptest.NewRequest(http.MethodPost, "/api/admin/integrations/safekeep/test", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("test safekeep connection: %d %s", rec.Code, rec.Body.String())
	}

	// 3. Toggle scheduled task enabled = true, then disabled, then enabled via PATCH /api/admin/scheduler/tasks
	for _, wantEnabled := range []bool{true, false, true} {
		patchBody, _ := json.Marshal(map[string]any{
			"name":    scheduler.TaskSafekeepMigrationBackup,
			"enabled": wantEnabled,
		})
		rec = httptest.NewRecorder()
		h.handleSchedulerTasks(rec, httptest.NewRequest(http.MethodPatch, "/api/admin/scheduler/tasks", bytes.NewReader(patchBody)))
		if rec.Code != http.StatusOK {
			t.Fatalf("toggle enabled=%v failed: %d %s", wantEnabled, rec.Code, rec.Body.String())
		}
		var taskView scheduler.TaskView
		_ = json.Unmarshal(rec.Body.Bytes(), &taskView)
		if taskView.Enabled != wantEnabled {
			t.Fatalf("expected enabled=%v, got %v", wantEnabled, taskView.Enabled)
		}
	}

	// 4. Trigger safekeep_migration_backup and verify the uploaded archive decodes cleanly with the configured passphrase
	runReq, _ := json.Marshal(map[string]string{"name": scheduler.TaskSafekeepMigrationBackup})
	rec = httptest.NewRecorder()
	h.handleSchedulerTaskRun(rec, httptest.NewRequest(http.MethodPost, "/api/admin/scheduler/tasks/run", bytes.NewReader(runReq)))
	if rec.Code != http.StatusOK {
		t.Fatalf("trigger safekeep backup task: %d %s", rec.Code, rec.Body.String())
	}
	if len(uploadedZip) == 0 || uploadedFilename == "" {
		t.Fatalf("expected non-empty migration archive upload to SafeKeep")
	}
	decoded, err := DecodeMigrationArchive(uploadedZip, "backup-secret-passphrase")
	if err != nil {
		t.Fatalf("DecodeMigrationArchive on uploaded zip failed: %v", err)
	}
	if decoded.Manifest.Format != SnapshotFormat || !decoded.Manifest.Encrypted {
		t.Fatalf("unexpected decoded manifest: %+v", decoded.Manifest)
	}

	// 5. Also verify manual backup endpoint works in knowledge mode
	manualReq, _ := json.Marshal(map[string]any{"mode": "knowledge", "include_deleted": false})
	rec = httptest.NewRecorder()
	h.handleSafekeepBackupNow(rec, httptest.NewRequest(http.MethodPost, "/api/admin/integrations/safekeep/backup", bytes.NewReader(manualReq)))
	if rec.Code != http.StatusOK {
		t.Fatalf("manual safekeep backup: %d %s", rec.Code, rec.Body.String())
	}
	decodedKnowledge, err := DecodeMigrationArchive(uploadedZip, "backup-secret-passphrase")
	if err != nil || decodedKnowledge.Manifest.Format != KnowledgeBundleFormat {
		t.Fatalf("expected knowledge bundle upload, got %+v err=%v", decodedKnowledge.Manifest, err)
	}
	_ = uuid.Nil
}
