package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuditAndSchedulerUpgrade(t *testing.T) {
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

	if _, err := tx.Exec(ctx, `CREATE SCHEMA audit_upgrade; SET LOCAL search_path = audit_upgrade, public`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"001_init.sql",
		"002_api_token_ciphertext.sql",
		"003_api_token_last_used.sql",
		"004_memory_events_outlive_memories.sql",
		"005_scoped_member_permissions.sql",
		"006_project_creation_permission.sql",
		"007_memory_agent_names.sql",
		"008_audit_and_scheduler.sql",
	} {
		src, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(src)); err != nil {
			t.Fatalf("execute %s: %v", name, err)
		}
	}

	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM information_schema.tables
		WHERE table_schema = 'audit_upgrade'
		  AND table_name IN ('audit_logs', 'audit_task_runs', 'scheduled_tasks', 'scheduled_task_runs')
	`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("expected 4 new audit/scheduler tables, got %d (err=%v)", count, err)
	}
}
