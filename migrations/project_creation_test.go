package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestProjectCreationUpgrade ensures existing members retain their roles without acquiring creation rights.
func TestProjectCreationUpgrade(t *testing.T) {
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
	if _, err := tx.Exec(ctx, `CREATE SCHEMA creation_upgrade; SET LOCAL search_path = creation_upgrade, public`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_init.sql", "002_api_token_ciphertext.sql", "003_api_token_last_used.sql", "004_memory_events_outlive_memories.sql", "005_scoped_member_permissions.sql"} {
		source, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(source)); err != nil {
			t.Fatal(err)
		}
	}
	tenant, member := uuid.New(), uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Upgrade')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,'api_token',$2,'Upgrade')`, member, member.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,'global_writer')`, tenant, member); err != nil {
		t.Fatal(err)
	}
	source, err := FS.ReadFile("006_project_creation_permission.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(source)); err != nil {
		t.Fatal(err)
	}
	var allowed bool
	var role string
	if err := tx.QueryRow(ctx, `SELECT role,can_create_project FROM tenant_members WHERE tenant_id=$1 AND principal_id=$2`, tenant, member).Scan(&role, &allowed); err != nil || allowed || role != "global_writer" {
		t.Fatalf("upgrade altered grants: role=%s allowed=%v err=%v", role, allowed, err)
	}
}
