package migrations

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestUnifiedSafekeepUploadUpgrade preserves system settings, schedules and history while clearing overrides.
func TestUnifiedSafekeepUploadUpgrade(t *testing.T) {
	dsn := os.Getenv("ENGRAM_MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_MIGRATION_TEST_DSN is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `CREATE SCHEMA unified_safekeep_upload; SET LOCAL search_path = unified_safekeep_upload, public`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"008_audit_and_scheduler.sql", "009_safekeep_integration.sql"} {
		src, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(src)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO safekeep_settings (default_mode, default_path, default_targets) VALUES ('async', 'global/path', 'global-upstream')`); err != nil {
		t.Fatal(err)
	}
	legacy := `{"migration_mode":"knowledge","include_deleted":false,"upload_mode":"sync","remote_path":"legacy/path","targets":"legacy-upstream"}`
	if _, err := tx.Exec(ctx, `INSERT INTO scheduled_tasks (name, enabled, interval_seconds, config_json, next_run_at, run_count) VALUES ('safekeep_migration_backup', true, 900, $1, '2030-01-01T00:00:00Z', 7), ('other_task', false, 60, $1, NULL, 0)`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO scheduled_task_runs (id, task_name, status, summary) VALUES ('00000000-0000-4000-8000-000000000001', 'safekeep_migration_backup', 'success', 'existing history')`); err != nil {
		t.Fatal(err)
	}
	src, err := FS.ReadFile("012_unified_safekeep_upload_settings.sql")
	if err != nil {
		t.Fatal(err)
	}
	// A repeat run must also leave the unified configuration unchanged.
	for range 2 {
		if _, err := tx.Exec(ctx, string(src)); err != nil {
			t.Fatal(err)
		}
	}
	var mode, path, targets string
	if err := tx.QueryRow(ctx, `SELECT default_mode, default_path, default_targets FROM safekeep_settings`).Scan(&mode, &path, &targets); err != nil || mode != "async" || path != "global/path" || targets != "global-upstream" {
		t.Fatalf("system settings changed: %q %q %q err=%v", mode, path, targets, err)
	}
	var raw []byte
	var schedulePreserved bool
	if err := tx.QueryRow(ctx, `SELECT config_json, enabled AND interval_seconds = 900 AND next_run_at = '2030-01-01T00:00:00Z' AND run_count = 7 FROM scheduled_tasks WHERE name = 'safekeep_migration_backup'`).Scan(&raw, &schedulePreserved); err != nil || !schedulePreserved {
		t.Fatalf("schedule changed: preserved=%v err=%v", schedulePreserved, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg) != 2 || cfg["migration_mode"] != "knowledge" || cfg["include_deleted"] != false {
		t.Fatalf("export choices changed or overrides remain: %v err=%v", cfg, err)
	}
	var untouched bool
	if err := tx.QueryRow(ctx, `SELECT config_json = $1::jsonb FROM scheduled_tasks WHERE name = 'other_task'`, legacy).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("unrelated task changed: preserved=%v err=%v", untouched, err)
	}
	var history int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduled_task_runs WHERE task_name = 'safekeep_migration_backup' AND summary = 'existing history'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history changed: count=%d err=%v", history, err)
	}
}
