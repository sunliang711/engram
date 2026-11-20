package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"engram/internal/audit"
	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"
	"engram/internal/scheduler"

	"github.com/google/uuid"
)

func TestAuditAndSchedulerHandlerValidationAndNilSafety(t *testing.T) {
	h := &Handler{}

	rec := httptest.NewRecorder()
	h.handleAuditLogs(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit/logs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil audit logs status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.handleSchedulerTasks(rec, httptest.NewRequest(http.MethodGet, "/api/admin/scheduler/tasks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil scheduler tasks status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.handleAuditLogs(rec, httptest.NewRequest(http.MethodPost, "/api/admin/audit/logs", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/admin/audit/logs = %d, want 405", rec.Code)
	}

	hWithSvc := &Handler{
		auditSvc: audit.NewService(nil, config.Default().Audit, nil, observability.NewLogger("error", nil)),
	}
	rec = httptest.NewRecorder()
	hWithSvc.handleAuditLogs(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit/logs?tenant_id=bad-uuid", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid tenant_id = %d, want 400", rec.Code)
	}
}

func TestAdminAuditAndSchedulerEndpointsWithDB(t *testing.T) {
	dsn := os.Getenv("ENGRAM_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_ADMIN_TEST_DSN is required for admin audit/scheduler PostgreSQL tests")
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

	metrics := observability.NewMetrics()
	log := observability.NewLogger("error", nil)
	auditSvc := audit.NewService(database.Pool, cfg.Audit, metrics, log)
	sched := scheduler.New(database.Pool, cfg.Scheduler, auditSvc, metrics, log)
	if err := sched.RegisterBuiltinTasks(cfg.Audit); err != nil {
		t.Fatal(err)
	}
	if err := sched.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		pool:      database.Pool,
		auditSvc:  auditSvc,
		scheduler: sched,
		log:       log,
	}

	// Create tenant via handler and verify audit log is recorded
	tenantName := "audit-admin-test-" + uuid.NewString()[:8]
	createBody, _ := json.Marshal(map[string]string{"name": tenantName})
	rec := httptest.NewRecorder()
	h.handleTenants(rec, httptest.NewRequest(http.MethodPost, "/api/admin/tenants", bytes.NewReader(createBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE tenant_id = $1 OR action LIKE 'scheduler.%' OR action LIKE 'audit.%'`, created.ID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, created.ID)
	})

	// Query audit logs filtered by tenant_id
	rec = httptest.NewRecorder()
	h.handleAuditLogs(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit/logs?tenant_id="+created.ID.String(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("query audit logs: %d %s", rec.Code, rec.Body.String())
	}
	var logResp audit.QueryResult
	if err := json.Unmarshal(rec.Body.Bytes(), &logResp); err != nil {
		t.Fatal(err)
	}
	if logResp.Total != 1 || logResp.Items[0].Action != "tenant.create" {
		t.Fatalf("expected tenant.create audit log, got %+v", logResp)
	}

	// Run an audit inspection task via HTTP
	taskReq, _ := json.Marshal(map[string]string{"task_type": audit.TaskCredentialHygiene, "tenant_id": created.ID.String()})
	rec = httptest.NewRecorder()
	h.handleAuditTaskRun(rec, httptest.NewRequest(http.MethodPost, "/api/admin/audit/tasks/run", bytes.NewReader(taskReq)))
	if rec.Code != http.StatusOK {
		t.Fatalf("run audit task: %d %s", rec.Code, rec.Body.String())
	}

	// Update scheduled task config (retention & interval) via PATCH /api/admin/scheduler/tasks
	patchBody, _ := json.Marshal(map[string]any{
		"name":     scheduler.TaskAuditLogCleanup,
		"enabled":  true,
		"interval": "30m",
		"config":   map[string]any{"retention": "24h", "batch_size": 200},
	})
	rec = httptest.NewRecorder()
	h.handleSchedulerTasks(rec, httptest.NewRequest(http.MethodPatch, "/api/admin/scheduler/tasks", bytes.NewReader(patchBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch scheduler task: %d %s", rec.Code, rec.Body.String())
	}

	// Insert an old log (48h ago) and trigger the scheduled task via POST /api/admin/scheduler/tasks/run
	expiredID := uuid.New()
	auditSvc.Record(ctx, audit.Entry{
		ID:        expiredID,
		TenantID:  &created.ID,
		Source:    audit.SourceSystem,
		Action:    "test.expired",
		Status:    audit.StatusSuccess,
		CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
	})

	runBody, _ := json.Marshal(map[string]string{"name": scheduler.TaskAuditLogCleanup})
	rec = httptest.NewRecorder()
	h.handleSchedulerTaskRun(rec, httptest.NewRequest(http.MethodPost, "/api/admin/scheduler/tasks/run", bytes.NewReader(runBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("trigger scheduler task: %d %s", rec.Code, rec.Body.String())
	}
	var triggerResp struct {
		Run scheduler.TaskRun `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &triggerResp); err != nil {
		t.Fatal(err)
	}
	if triggerResp.Run.AffectedRows < 1 {
		t.Fatalf("expected >= 1 deleted expired audit log, got %+v", triggerResp.Run)
	}
}
