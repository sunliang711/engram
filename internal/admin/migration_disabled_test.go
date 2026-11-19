package admin

import (
	"context"
	"os"
	"testing"

	"engram/internal/auth"
	"engram/internal/config"
	"engram/internal/db"

	"github.com/google/uuid"
)

// TestKnowledgeImportDisabledTenants preserves disabled tenants while resolving console audit identities.
func TestKnowledgeImportDisabledTenants(t *testing.T) {
	dsn := os.Getenv("ENGRAM_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_ADMIN_TEST_DSN is required")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler := &Handler{pool: database.Pool}
	authenticator, err := auth.NewAuthenticator(database.Pool, cfg.Auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"new by name", "existing by name", "single", "tenant map"} {
		t.Run(mode, func(t *testing.T) {
			bundle := sampleKnowledgeBundlePayload()
			bundle.Tenants[0].Status = "disabled"
			for i := range bundle.Tenants {
				bundle.Tenants[i].Name = "disabled-import-" + uuid.NewString()
			}
			target := bundle.Tenants[0].ID
			if mode != "new by name" {
				target = uuid.New()
			}
			cleanupIDs := []uuid.UUID{target, bundle.Tenants[1].ID}
			t.Cleanup(func() {
				for _, statement := range []string{
					`DELETE FROM memory_events WHERE tenant_id = ANY($1)`,
					`DELETE FROM memory_revisions WHERE tenant_id = ANY($1)`,
					`DELETE FROM memories WHERE tenant_id = ANY($1)`,
					`DELETE FROM project_repositories WHERE tenant_id = ANY($1)`,
					`DELETE FROM projects WHERE tenant_id = ANY($1)`,
					`DELETE FROM tenants WHERE id = ANY($1)`,
				} {
					if _, err := database.Pool.Exec(ctx, statement, cleanupIDs); err != nil {
						t.Errorf("cleanup disabled import: %v", err)
					}
				}
			})
			if mode != "new by name" {
				if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants(id,name,status) VALUES($1,$2,'disabled')`, target, bundle.Tenants[0].Name); err != nil {
					t.Fatal(err)
				}
			}
			opts := KnowledgeImportOptions{TenantMode: "by_name"}
			switch mode {
			case "single":
				opts = KnowledgeImportOptions{TenantMode: "single", TargetTenantID: target}
			case "tenant map":
				opts.TenantMap = map[string]string{bundle.Tenants[0].ID.String(): target.String()}
				// Exercise explicit mapping instead of accidentally matching the original name.
				if _, err := database.Pool.Exec(ctx, `UPDATE tenants SET name = $2 WHERE id = $1`, target, "mapped-"+target.String()); err != nil {
					t.Fatal(err)
				}
			}
			counts, err := handler.importKnowledgeBundlePayload(ctx, bundle, opts)
			if err != nil {
				t.Fatalf("import disabled tenant: %v", err)
			}
			if counts.MemoriesImported != 2 || counts.RevisionsImported != 3 || counts.MemoryEvents != 2 {
				t.Fatalf("incomplete import: %+v", counts)
			}
			var status string
			if err := database.Pool.QueryRow(ctx, `SELECT status FROM tenants WHERE id = $1`, target).Scan(&status); err != nil || status != "disabled" {
				t.Fatalf("disabled status changed: %s, %v", status, err)
			}
			var actor uuid.UUID
			if err := database.Pool.QueryRow(ctx, `SELECT created_by FROM memories WHERE id = $1 AND tenant_id = $2`, bundle.Memories[0].ID, target).Scan(&actor); err != nil {
				t.Fatal(err)
			}
			var invalidActors int
			if err := database.Pool.QueryRow(ctx, `
				SELECT count(*) FROM (
					SELECT created_by AS actor_id FROM memories WHERE tenant_id = ANY($1)
					UNION ALL SELECT updated_by FROM memories WHERE tenant_id = ANY($1)
					UNION ALL SELECT actor_id FROM memory_revisions WHERE tenant_id = ANY($1)
					UNION ALL SELECT actor_id FROM memory_events WHERE tenant_id = ANY($1)
				) audit JOIN principals p ON p.id = audit.actor_id
				WHERE p.identity_provider <> 'system_webui' OR p.subject <> 'admin'
			`, cleanupIDs).Scan(&invalidActors); err != nil || invalidActors != 0 {
				t.Fatalf("invalid audit actors: %d, %v", invalidActors, err)
			}
			var grants int
			if err := database.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tenant_members WHERE principal_id = $1) + (SELECT count(*) FROM project_members WHERE principal_id = $1)`, actor).Scan(&grants); err != nil || grants != 0 {
				t.Fatalf("audit identity received member grants: %d, %v", grants, err)
			}
			// Migration may write disabled tenants; interactive management must still reject them.
			if _, err := handler.ensureAdminActorForTenant(ctx, target); err == nil {
				t.Fatal("interactive console accepted disabled tenant")
			}
			if _, err := authenticator.VerifyAdminActor(ctx, target, actor); err == nil {
				t.Fatal("management service accepted disabled tenant")
			}
		})
	}
}
