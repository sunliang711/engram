package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMemoryAgentUpgrade leaves existing memories unattributed instead of inventing an agent identity.
func TestMemoryAgentUpgrade(t *testing.T) {
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
	if _, err := tx.Exec(ctx, `CREATE SCHEMA agent_upgrade; SET LOCAL search_path = agent_upgrade, public`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_init.sql", "002_api_token_ciphertext.sql", "003_api_token_last_used.sql", "004_memory_events_outlive_memories.sql", "005_scoped_member_permissions.sql", "006_project_creation_permission.sql"} {
		source, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(source)); err != nil {
			t.Fatal(err)
		}
	}
	tenant, member, memoryID := uuid.New(), uuid.New(), uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Upgrade')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,'api_token',$2,'Owner')`, member, member.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memories(id,tenant_id,scope,category,title,content_md,content_hash,created_by,updated_by) VALUES($1,$2,'global','testing','Legacy','body','hash',$3,$3)`, memoryID, tenant, member); err != nil {
		t.Fatal(err)
	}
	source, err := FS.ReadFile("007_memory_agent_names.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(source)); err != nil {
		t.Fatal(err)
	}
	var created, updated string
	var createdBy, updatedBy uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT created_agent,updated_agent,created_by,updated_by FROM memories WHERE id=$1`, memoryID).Scan(&created, &updated, &createdBy, &updatedBy); err != nil || created != "" || updated != "" || createdBy != member || updatedBy != member {
		t.Fatalf("upgrade altered attribution: created=%q updated=%q members=%s/%s err=%v", created, updated, createdBy, updatedBy, err)
	}
}
