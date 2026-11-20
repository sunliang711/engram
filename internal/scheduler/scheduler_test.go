package scheduler

import (
	"context"
	"os"
	"testing"
	"time"

	"engram/internal/audit"
	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"

	"github.com/google/uuid"
)

func TestValidateAuditCleanupConfig(t *testing.T) {
	validCases := []map[string]any{
		{"retention": "30d", "batch_size": 1000},
		{"retention": "720h", "batch_size": float64(500)},
		{"retention": "5m"},
	}
	for _, tc := range validCases {
		out, err := validateAuditCleanupConfig(tc)
		if err != nil {
			t.Fatalf("expected valid config %+v, got error: %v", tc, err)
		}
		if out["retention"] == "" || out["batch_size"].(int) < minBatchSize {
			t.Fatalf("unexpected normalized output: %+v", out)
		}
	}

	invalidCases := []map[string]any{
		{},
		{"retention": ""},
		{"retention": "10s"},
		{"retention": "not-a-duration"},
		{"retention": "30d", "batch_size": 50},
		{"retention": "30d", "batch_size": 50000},
	}
	for _, tc := range invalidCases {
		if _, err := validateAuditCleanupConfig(tc); err == nil {
			t.Fatalf("expected validation error for %+v", tc)
		}
	}
}

func TestSchedulerSyncUpdateTriggerAndRunDue(t *testing.T) {
	dsn := os.Getenv("ENGRAM_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_TEST_DSN is required for scheduler PostgreSQL tests")
	}
	ctx := context.Background()
	dbCfg := config.Default().Database
	dbCfg.DSN = dsn
	dbCfg.AutoMigrate = true
	database, err := db.Open(ctx, dbCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	metrics := observability.NewMetrics()
	log := observability.NewLogger("error", nil)
	auditSvc := audit.NewService(database.Pool, config.AuditConfig{
		Enabled:   true,
		Retention: 30 * 24 * time.Hour,
		LogReads:  true,
	}, metrics, log)

	schedCfg := config.SchedulerConfig{
		Enabled:               true,
		AuditCleanupEnabled:   true,
		AuditCleanupInterval:  time.Hour,
		AuditCleanupBatchSize: 1000,
	}
	sched := New(database.Pool, schedCfg, auditSvc, metrics, log)
	if err := sched.RegisterBuiltinTasks(config.AuditConfig{Retention: 30 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := sched.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	task, err := sched.GetTask(ctx, TaskAuditLogCleanup)
	if err != nil {
		t.Fatal(err)
	}
	if !task.Enabled || task.IntervalSeconds != 3600 {
		t.Fatalf("unexpected initial task state: %+v", task)
	}

	// Update retention to 7d and interval to 15m, then verify Sync preserves runtime edits.
	enabled := true
	updated, err := sched.UpdateTask(ctx, UpdateTaskRequest{
		Name:     TaskAuditLogCleanup,
		Enabled:  &enabled,
		Interval: "15m",
		Config:   map[string]any{"retention": "7d", "batch_size": 500},
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if updated.IntervalSeconds != 900 || updated.Config["retention"] != "7d" {
		t.Fatalf("unexpected updated task: %+v", updated)
	}
	if err := sched.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	reloaded, err := sched.GetTask(ctx, TaskAuditLogCleanup)
	if err != nil || reloaded.IntervalSeconds != 900 || reloaded.Config["retention"] != "7d" {
		t.Fatalf("Sync overwrote persisted runtime config: %+v, err=%v", reloaded, err)
	}

	// Insert an 8-day-old audit log and a fresh audit log; trigger cleanup and verify only expired log is deleted.
	oldID, freshID := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE id IN ($1, $2) OR resource_id = $3`, oldID, freshID, TaskAuditLogCleanup)
	})
	auditSvc.Record(ctx, audit.Entry{
		ID:        oldID,
		ActorType: audit.ActorSystem,
		Source:    audit.SourceSystem,
		Action:    "test.old_entry",
		Status:    audit.StatusSuccess,
		CreatedAt: time.Now().UTC().Add(-8 * 24 * time.Hour),
	})
	auditSvc.Record(ctx, audit.Entry{
		ID:        freshID,
		ActorType: audit.ActorSystem,
		Source:    audit.SourceSystem,
		Action:    "test.fresh_entry",
		Status:    audit.StatusSuccess,
		CreatedAt: time.Now().UTC(),
	})

	run, err := sched.TriggerTask(ctx, TaskAuditLogCleanup, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusSuccess || run.AffectedRows < 1 {
		t.Fatalf("expected manual trigger to delete old entry, got %+v", run)
	}

	var oldExists, freshExists bool
	_ = database.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_logs WHERE id = $1)`, oldID).Scan(&oldExists)
	_ = database.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_logs WHERE id = $1)`, freshID).Scan(&freshExists)
	if oldExists || !freshExists {
		t.Fatalf("retention cleanup mismatch: oldExists=%v freshExists=%v", oldExists, freshExists)
	}

	// Make task due and run via RunDue
	if _, err := database.Pool.Exec(ctx, `UPDATE scheduled_tasks SET next_run_at = now() - interval '1 minute' WHERE name = $1`, TaskAuditLogCleanup); err != nil {
		t.Fatal(err)
	}
	if err := sched.RunDue(ctx); err != nil {
		t.Fatal(err)
	}

	runs, total, err := sched.ListRuns(ctx, TaskAuditLogCleanup, 1, 10)
	if err != nil || total < 2 || len(runs) < 2 {
		t.Fatalf("expected >= 2 recorded runs, got len=%d total=%d err=%v", len(runs), total, err)
	}
}
