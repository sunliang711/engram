package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/exporter"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// TestAgentMigrationCompatibility keeps new attribution optional in both archive payload formats.
func TestAgentMigrationCompatibility(t *testing.T) {
	for _, raw := range []string{`{"memories":[{"title":"Legacy"}]}`, `{"memories":[{"title":"Legacy","created_agent":"Codex","updated_agent":"review-agent"}]}`} {
		var snapshot SnapshotPayload
		var bundle KnowledgeBundlePayload
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
			t.Fatal(err)
		}
		if snapshot.Memories[0].CreatedAgent != bundle.Memories[0].CreatedAgent || snapshot.Memories[0].UpdatedAgent != bundle.Memories[0].UpdatedAgent {
			t.Fatal("snapshot and knowledge bundle disagree on Agent metadata")
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip SnapshotPayload
		if err := json.Unmarshal(encoded, &roundTrip); err != nil || roundTrip.Memories[0].CreatedAgent != snapshot.Memories[0].CreatedAgent || roundTrip.Memories[0].UpdatedAgent != snapshot.Memories[0].UpdatedAgent {
			t.Fatalf("Agent metadata changed during JSON round trip: %+v, %v", roundTrip.Memories, err)
		}
	}
}

// TestAgentMigrationPostgreSQL replaces only the explicitly configured disposable snapshot test database.
func TestAgentMigrationPostgreSQL(t *testing.T) {
	dsn := os.Getenv("ENGRAM_AGENT_MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_AGENT_MIGRATION_TEST_DSN must point to a disposable database; this test restores full snapshots")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler := &Handler{pool: database.Pool, authMgr: NewAuthManager(database.Pool, cfg.Server.ContextSignKey, cfg.Server.ContextTTL, false)}
	payload := sampleSnapshotPayload()
	payload.Memories[0].CreatedAgent = "Codex"
	payload.Memories[0].UpdatedAgent = "review-agent: \"审核\""
	payload.MemoryRevisions[0].Snapshot = json.RawMessage(`{"title":"UAT Address","created_agent":"Codex","updated_agent":"review-agent: \"审核\""}`)
	if _, err := handler.importSnapshotPayload(ctx, payload, true); err != nil {
		t.Fatal(err)
	}
	exported, err := handler.exportSnapshotPayload(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Memories) != 1 || exported.Memories[0].CreatedAgent != "Codex" || exported.Memories[0].UpdatedAgent != payload.Memories[0].UpdatedAgent {
		t.Fatalf("snapshot export lost attribution: %+v", exported.Memories)
	}
	encoded, _, err := EncodeSnapshotArchive(exported, "", payload.Tenants[0].CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	_, restored, err := DecodeSnapshotArchive(encoded, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.importSnapshotPayload(ctx, restored, true); err != nil {
		t.Fatal(err)
	}
	bundle, err := handler.exportKnowledgeBundlePayload(ctx, KnowledgeExportOptions{TenantIDs: []uuid.UUID{payload.Tenants[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Memories) != 1 || bundle.Memories[0].CreatedAgent != "Codex" || bundle.Memories[0].UpdatedAgent != payload.Memories[0].UpdatedAgent {
		t.Fatalf("knowledge export lost attribution: %+v", bundle.Memories)
	}

	// Older snapshots omit both fields and must restore empty attribution rather than invent an Agent.
	legacy := payload
	legacy.Memories = append([]snapshotMemory(nil), payload.Memories...)
	legacy.Memories[0].CreatedAgent = ""
	legacy.Memories[0].UpdatedAgent = ""
	if _, err := handler.importSnapshotPayload(ctx, legacy, true); err != nil {
		t.Fatal(err)
	}
	var created, updated string
	if err := database.Pool.QueryRow(ctx, `SELECT created_agent,updated_agent FROM memories WHERE id=$1`, legacy.Memories[0].ID).Scan(&created, &updated); err != nil || created != "" || updated != "" {
		t.Fatalf("legacy snapshot attribution: created=%q updated=%q err=%v", created, updated, err)
	}

	targetTenant := uuid.New()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Agent import target')`, targetTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.importKnowledgeBundlePayload(ctx, bundle, KnowledgeImportOptions{TenantMode: "single", TargetTenantID: targetTenant}); err != nil {
		t.Fatal(err)
	}
	var importedMemory uuid.UUID
	if err := database.Pool.QueryRow(ctx, `SELECT id,created_agent,updated_agent FROM memories WHERE tenant_id=$1`, targetTenant).Scan(&importedMemory, &created, &updated); err != nil || created != "Codex" || updated != payload.Memories[0].UpdatedAgent {
		t.Fatalf("knowledge import attribution: created=%q updated=%q err=%v", created, updated, err)
	}
	var revision json.RawMessage
	if err := database.Pool.QueryRow(ctx, `SELECT snapshot FROM memory_revisions WHERE tenant_id=$1`, targetTenant).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		CreatedAgent string `json:"created_agent"`
		UpdatedAgent string `json:"updated_agent"`
	}
	if err := json.Unmarshal(revision, &snapshot); err != nil || snapshot.CreatedAgent != "Codex" || snapshot.UpdatedAgent != payload.Memories[0].UpdatedAgent {
		t.Fatalf("knowledge revision attribution changed: %+v, %v", snapshot, err)
	}

	// Query-backed Markdown and ZIP paths must preserve the same metadata as portable JSON archives.
	exp := exporter.NewExporter(database.Pool, config.ExportConfig{OutputDir: t.TempDir()}, nil, zerolog.Nop())
	if _, err := exp.ExportAll(ctx); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	items, err := exp.ExportFilteredArchive(ctx, &archive, exporter.ArchiveFilter{MemoryID: &importedMemory}, nil)
	if err != nil || len(items) != 1 || items[0].CreatedAgent != "Codex" || items[0].UpdatedAgent != payload.Memories[0].UpdatedAgent {
		t.Fatalf("Markdown archive manifest lost attribution: %+v, %v", items, err)
	}
	if !strings.Contains(items[0].Path, importedMemory.String()) {
		t.Fatalf("unexpected archive item: %+v", items[0])
	}
}
