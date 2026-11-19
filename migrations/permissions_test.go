package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestScopedPermissionUpgrade checks legacy data conversion without touching any application database.
func TestScopedPermissionUpgrade(t *testing.T) {
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
	if _, err := tx.Exec(ctx, `CREATE SCHEMA permissions_upgrade; SET LOCAL search_path = permissions_upgrade, public`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_init.sql", "002_api_token_ciphertext.sql", "003_api_token_last_used.sql", "004_memory_events_outlive_memories.sql"} {
		source, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(source)); err != nil {
			t.Fatal(err)
		}
	}
	tenant, otherTenant, formerAdmin, ordinary, system, project, otherProject := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{tenant, otherTenant} {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Upgrade')`, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uuid.UUID{formerAdmin, ordinary, system} {
		provider := "api_token"
		if id == system {
			provider = "system_webui"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,$2,$3,'Upgrade')`, id, provider, id.String()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,'admin'),($1,$3,'member'),($1,$4,'admin'),($5,$2,'global_writer')`, tenant, formerAdmin, ordinary, system, otherTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO projects(id,tenant_id,name) VALUES($1,$2,'P'),($3,$4,'Other')`, project, tenant, otherProject, otherTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_members(project_id,principal_id,role) VALUES($1,$2,'reader'),($1,$3,'maintainer'),($1,$4,'maintainer'),($5,$2,'reader')`, project, formerAdmin, ordinary, system, otherProject); err != nil {
		t.Fatal(err)
	}
	source, err := FS.ReadFile("005_scoped_member_permissions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(source)); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := tx.QueryRow(ctx, `SELECT role FROM tenant_members WHERE tenant_id=$1 AND principal_id=$2`, tenant, formerAdmin).Scan(&role); err != nil || role != "none" {
		t.Fatalf("former admin: role=%s err=%v", role, err)
	}
	var grants int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_members WHERE project_id=$1 AND principal_id=$2`, project, formerAdmin).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("former admin grants=%d err=%v", grants, err)
	}
	if err := tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND principal_id=$2`, project, ordinary).Scan(&role); err != nil || role != "writer" {
		t.Fatalf("maintainer conversion: role=%s err=%v", role, err)
	}
	if err := tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND principal_id=$2`, otherProject, formerAdmin).Scan(&role); err != nil || role != "reader" {
		t.Fatalf("other tenant access: role=%s err=%v", role, err)
	}
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM tenant_members WHERE principal_id=$1) + (SELECT count(*) FROM project_members WHERE principal_id=$1)`, system).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("console grants=%d err=%v", grants, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,'admin')`, otherTenant, ordinary); err == nil {
		t.Fatal("obsolete admin role accepted")
	}
}
