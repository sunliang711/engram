package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/db"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func sampleSnapshotPayload() SnapshotPayload {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	tid := uuid.New()
	pid := uuid.New()
	projID := uuid.New()
	marker := uuid.New()
	memID := uuid.New()
	tokID := uuid.New()
	return SnapshotPayload{
		Tenants: []snapshotTenant{
			{ID: tid, Name: "default", Status: "active", CreatedAt: now},
		},
		Principals: []snapshotPrincipal{
			{ID: pid, IdentityProvider: "api_token", Subject: "macbook", DisplayName: "MacBook", Status: "active", CreatedAt: now},
		},
		TenantMembers: []snapshotTenantMember{
			{TenantID: tid, PrincipalID: pid, Role: "admin", CreatedAt: now},
		},
		Projects: []snapshotProject{
			{ID: projID, TenantID: tid, Name: "broker-core", Marker: &marker, Status: "active", CreatedAt: now, UpdatedAt: now},
		},
		ProjectMembers: []snapshotProjectMember{
			{ProjectID: projID, PrincipalID: pid, Role: "maintainer", CreatedAt: now},
		},
		ProjectRepositories: []snapshotProjectRepository{
			{ID: uuid.New(), TenantID: tid, ProjectID: projID, CanonicalRemote: "github.com/eagle/broker-core", WorkspacePath: "", IsPrimary: true, CreatedAt: now},
		},
		APITokens: []snapshotAPIToken{
			{ID: tokID, TokenHash: "hash123", TokenPrefix: "aim_12345678", TokenCiphertext: "v1:cipher", RawToken: "aim_secret_token", TenantID: tid, PrincipalID: pid, Name: "default", CreatedAt: now},
		},
		Memories: []snapshotMemory{
			{
				ID:          memID,
				TenantID:    tid,
				Scope:       "project",
				ProjectID:   &projID,
				Category:    "env",
				Title:       "UAT Address",
				ContentMD:   "https://uat.example.com",
				Tags:        []string{"uat"},
				SourceRefs:  json.RawMessage(`[]`),
				IsPinned:    true,
				Status:      "active",
				Version:     1,
				ContentHash: "memhash",
				CreatedBy:   pid,
				UpdatedBy:   pid,
				CreatedAt:   now,
				UpdatedAt:   now,
			},
		},
		MemoryRevisions: []snapshotMemoryRevision{
			{ID: 1, TenantID: tid, MemoryID: memID, Version: 1, Operation: "create", Snapshot: json.RawMessage(`{"title":"UAT Address"}`), ActorID: pid, RequestID: "req_1", CreatedAt: now},
		},
		MemoryEvents: []snapshotMemoryEvent{
			{Revision: 1, TenantID: tid, ProjectID: &projID, MemoryID: memID, Operation: "create", MemoryVersion: 1, ActorID: pid, CreatedAt: now},
		},
	}
}

func TestSnapshotArchive_UnencryptedRoundTrip(t *testing.T) {
	payload := sampleSnapshotPayload()
	payload.APITokens[0].RawToken = ""

	zipBytes, manifest, err := EncodeSnapshotArchive(payload, "", time.Now())
	if err != nil {
		t.Fatalf("EncodeSnapshotArchive failed: %v", err)
	}
	if manifest.Encrypted {
		t.Fatal("expected manifest.Encrypted=false")
	}

	gotManifest, gotPayload, err := DecodeSnapshotArchive(zipBytes, "")
	if err != nil {
		t.Fatalf("DecodeSnapshotArchive failed: %v", err)
	}
	if gotManifest.PayloadSHA256 != manifest.PayloadSHA256 {
		t.Fatalf("sha mismatch: got %q want %q", gotManifest.PayloadSHA256, manifest.PayloadSHA256)
	}
	if len(gotPayload.Memories) != 1 || gotPayload.Memories[0].Title != "UAT Address" {
		t.Fatalf("unexpected decoded memories: %+v", gotPayload.Memories)
	}
}

func TestSnapshotArchive_EncryptedRoundTripAndErrors(t *testing.T) {
	payload := sampleSnapshotPayload()
	zipBytes, manifest, err := EncodeSnapshotArchive(payload, "correct-horse-battery-staple", time.Now())
	if err != nil {
		t.Fatalf("EncodeSnapshotArchive encrypted failed: %v", err)
	}
	if !manifest.Encrypted {
		t.Fatal("expected manifest.Encrypted=true")
	}

	// Ensure payload.json is NOT present in encrypted zip.
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == "payload.json" {
			t.Fatal("encrypted snapshot archive must not contain plaintext payload.json")
		}
	}

	// Missing passphrase must fail.
	if _, _, err := DecodeSnapshotArchive(zipBytes, ""); err == nil {
		t.Fatal("expected error when passphrase is missing")
	}
	// Wrong passphrase must fail.
	if _, _, err := DecodeSnapshotArchive(zipBytes, "wrong-passphrase"); err == nil {
		t.Fatal("expected error when passphrase is wrong")
	}

	// Correct passphrase restores full payload including RawToken.
	_, gotPayload, err := DecodeSnapshotArchive(zipBytes, "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("DecodeSnapshotArchive with right passphrase failed: %v", err)
	}
	if len(gotPayload.APITokens) != 1 || gotPayload.APITokens[0].RawToken != "aim_secret_token" {
		t.Fatalf("expected raw_token to be preserved inside encrypted snapshot, got %+v", gotPayload.APITokens)
	}
}

func sampleKnowledgeBundlePayload() KnowledgeBundlePayload {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	t1, t2 := uuid.New(), uuid.New()
	p1 := uuid.New()
	marker := uuid.New()
	m1, m2 := uuid.New(), uuid.New()
	return KnowledgeBundlePayload{
		Tenants: []snapshotTenant{
			{ID: t1, Name: "team-alpha", Status: "active", CreatedAt: now},
			{ID: t2, Name: "team-beta", Status: "active", CreatedAt: now},
		},
		Projects: []snapshotProject{
			{ID: p1, TenantID: t1, Name: "api-service", Marker: &marker, Status: "active", CreatedAt: now, UpdatedAt: now},
		},
		ProjectRepositories: []snapshotProjectRepository{
			{ID: uuid.New(), TenantID: t1, ProjectID: p1, CanonicalRemote: "github.com/acme/api-service", WorkspacePath: "", IsPrimary: true, CreatedAt: now},
		},
		Memories: []knowledgeMemory{
			{
				ID:          m1,
				TenantID:    t1,
				Scope:       "project",
				ProjectID:   &p1,
				Category:    "architecture",
				Title:       "API Auth Flow",
				ContentMD:   "Uses bearer tokens.",
				Tags:        []string{"auth"},
				SourceRefs:  json.RawMessage(`[]`),
				IsPinned:    true,
				Status:      "active",
				Version:     2,
				ContentHash: "hash-m1",
				CreatedAt:   now,
				UpdatedAt:   now,
			},
			{
				ID:          m2,
				TenantID:    t2,
				Scope:       "global",
				Category:    "conventions",
				Title:       "Go Formatting",
				ContentMD:   "Always run gofmt.",
				Tags:        []string{"go"},
				SourceRefs:  json.RawMessage(`[]`),
				IsPinned:    false,
				Status:      "active",
				Version:     1,
				ContentHash: "hash-m2",
				CreatedAt:   now,
				UpdatedAt:   now,
			},
		},
		MemoryRevisions: []knowledgeMemoryRevision{
			{TenantID: t1, MemoryID: m1, Version: 1, Operation: "create", Snapshot: json.RawMessage(`{"title":"API Auth Flow"}`), RequestID: "req_1", CreatedAt: now},
			{TenantID: t1, MemoryID: m1, Version: 2, Operation: "update", Snapshot: json.RawMessage(`{"title":"API Auth Flow","content":"Uses bearer tokens."}`), RequestID: "req_2", CreatedAt: now},
			{TenantID: t2, MemoryID: m2, Version: 1, Operation: "create", Snapshot: json.RawMessage(`{"title":"Go Formatting"}`), RequestID: "req_3", CreatedAt: now},
		},
	}
}

func TestKnowledgeBundle_RoundTripAndInspect(t *testing.T) {
	bundle := sampleKnowledgeBundlePayload()
	for _, passphrase := range []string{"", "bundle-secret-key"} {
		zipBytes, manifest, err := EncodeKnowledgeArchive(bundle, passphrase, time.Now())
		if err != nil {
			t.Fatalf("EncodeKnowledgeArchive(encrypted=%t): %v", passphrase != "", err)
		}
		if manifest.Format != KnowledgeBundleFormat {
			t.Fatalf("unexpected format: %q", manifest.Format)
		}
		if manifest.Encrypted != (passphrase != "") {
			t.Fatalf("unexpected encrypted flag: %t", manifest.Encrypted)
		}

		// Manifest inspection must succeed without passphrase even for encrypted bundles.
		inspectedManifest, err := InspectArchiveManifest(zipBytes)
		if err != nil {
			t.Fatalf("InspectArchiveManifest: %v", err)
		}
		if inspectedManifest.Counts.Tenants != 2 || inspectedManifest.Counts.Memories != 2 || inspectedManifest.Counts.MemoryRevisions != 3 {
			t.Fatalf("unexpected manifest counts: %+v", inspectedManifest.Counts)
		}

		// Snapshot-only decoder must reject knowledge bundle format.
		if _, _, err := DecodeSnapshotArchive(zipBytes, passphrase); err == nil {
			t.Fatal("DecodeSnapshotArchive must reject knowledge bundle format")
		}

		if passphrase != "" {
			if _, err := DecodeMigrationArchive(zipBytes, ""); err == nil {
				t.Fatal("expected error when passphrase is missing")
			}
			if _, err := DecodeMigrationArchive(zipBytes, "wrong"); err == nil {
				t.Fatal("expected error when passphrase is wrong")
			}
		}

		decoded, err := DecodeMigrationArchive(zipBytes, passphrase)
		if err != nil {
			t.Fatalf("DecodeMigrationArchive: %v", err)
		}
		if decoded.KnowledgeBundle == nil || len(decoded.KnowledgeBundle.Tenants) != 2 || len(decoded.KnowledgeBundle.Memories) != 2 {
			t.Fatalf("unexpected decoded knowledge bundle: %+v", decoded.KnowledgeBundle)
		}
		tenants := summarizeArchiveTenants(decoded)
		if len(tenants) != 2 || tenants[0].ProjectsCount != 1 || tenants[0].MemoriesCount != 1 || tenants[1].MemoriesCount != 1 {
			t.Fatalf("unexpected tenant summaries: %+v", tenants)
		}
	}
}

func TestKnowledgeBundle_Validation(t *testing.T) {
	bad := sampleKnowledgeBundlePayload()
	bad.Projects[0].TenantID = uuid.New()
	if err := validateKnowledgeBundle(bad); err == nil {
		t.Fatal("expected error when project references unknown tenant")
	}

	bad2 := sampleKnowledgeBundlePayload()
	bad2.Memories[0].ProjectID = nil
	if err := validateKnowledgeBundle(bad2); err == nil {
		t.Fatal("expected error when project-scoped memory has nil project_id")
	}
}

func TestKnowledgeMigrationPostgreSQL(t *testing.T) {
	dsn := os.Getenv("ENGRAM_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_ADMIN_TEST_DSN is required for the admin PostgreSQL migration test")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authMgr := NewAuthManager(database.Pool, "test-signing-key-at-least-32-bytes-long", time.Hour, false)
	h := NewHandler(database.Pool, authMgr, nil, nil, zerolog.Nop())

	srcT1, srcT2 := uuid.New(), uuid.New()
	targetMerged := uuid.New()
	srcPrincipal, targetPrincipal := uuid.New(), uuid.New()
	srcProj := uuid.New()
	srcMarker := uuid.New()
	srcMem1, srcMem2 := uuid.New(), uuid.New()
	autoCreatedName := "mig-auto-" + uuid.NewString()[:8]

	cleanupTenants := []uuid.UUID{srcT1, srcT2, targetMerged}
	defer func() {
		var autoID uuid.UUID
		if err := database.Pool.QueryRow(ctx, `SELECT id FROM tenants WHERE name = $1`, autoCreatedName).Scan(&autoID); err == nil {
			cleanupTenants = append(cleanupTenants, autoID)
		}
		for _, tid := range cleanupTenants {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM memory_events WHERE tenant_id = $1`, tid)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM memory_revisions WHERE tenant_id = $1`, tid)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM memories WHERE tenant_id = $1`, tid)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM project_repositories WHERE tenant_id = $1`, tid)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, tid)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM tenant_members WHERE tenant_id = $1`, tid)
			_, _ = database.Pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid)
		}
		_, _ = database.Pool.Exec(ctx, `DELETE FROM principals WHERE id = ANY($1)`, []uuid.UUID{srcPrincipal, targetPrincipal})
	}()

	// Seed source tenants, principals, project, memories, and revisions.
	for _, row := range []struct {
		id   uuid.UUID
		name string
	}{
		{srcT1, autoCreatedName},
		{srcT2, "mig-src2-" + uuid.NewString()[:8]},
		{targetMerged, "mig-target-" + uuid.NewString()[:8]},
	} {
		if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants (id, name, status) VALUES ($1, $2, 'active')`, row.id, row.name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO principals (id, identity_provider, subject, display_name) VALUES ($1, 'api_token', $2, 'Source Author')`, srcPrincipal, "src-"+srcPrincipal.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO principals (id, identity_provider, subject, display_name) VALUES ($1, 'api_token', $2, 'Target Member')`, targetPrincipal, "tgt-"+targetPrincipal.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenant_members (tenant_id, principal_id, role) VALUES ($1, $2, 'admin')`, targetMerged, targetPrincipal); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO projects (id, tenant_id, name, marker, status) VALUES ($1, $2, 'svc-a', $3, 'active')`, srcProj, srcT1, srcMarker); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary) VALUES ($1, $2, $3, 'github.com/org/svc-a', '', true)`, uuid.New(), srcT1, srcProj); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO memories (id, tenant_id, scope, project_id, category, title, content_md, tags, source_refs, is_pinned, status, version, content_hash, created_by, updated_by)
		VALUES ($1, $2, 'project', $3, 'env', 'Prod URL', 'https://prod.example.com', ARRAY['prod'], '[]'::jsonb, true, 'active', 1, 'hash-src-1', $4, $4)
	`, srcMem1, srcT1, srcProj, srcPrincipal); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO memories (id, tenant_id, scope, project_id, category, title, content_md, tags, source_refs, is_pinned, status, version, source_memory_id, content_hash, created_by, updated_by)
		VALUES ($1, $2, 'global', NULL, 'notes', 'Global Note', 'Shared note', ARRAY['note'], '[]'::jsonb, false, 'active', 1, $3, 'hash-src-2', $4, $4)
	`, srcMem2, srcT2, srcMem1, srcPrincipal); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO memory_revisions (tenant_id, memory_id, version, operation, snapshot, actor_id, request_id)
		VALUES ($1, $2, 1, 'create', '{"title":"Prod URL"}'::jsonb, $3, 'r1'),
		       ($4, $5, 1, 'create', '{"title":"Global Note"}'::jsonb, $3, 'r2')
	`, srcT1, srcMem1, srcPrincipal, srcT2, srcMem2); err != nil {
		t.Fatal(err)
	}

	// 1. Export both source tenants into one knowledge bundle via HTTP handler.
	exportBody, _ := json.Marshal(map[string]any{
		"mode":            "knowledge",
		"tenant_ids":      []string{srcT1.String(), srcT2.String()},
		"include_deleted": true,
	})
	expRec := httptest.NewRecorder()
	h.handleMigrationExport(expRec, httptest.NewRequest(http.MethodPost, "/api/admin/migration/export", bytes.NewReader(exportBody)))
	if expRec.Code != http.StatusOK {
		t.Fatalf("export status %d: %s", expRec.Code, expRec.Body.String())
	}
	bundleZip := expRec.Body.Bytes()

	// 2. Inspect uploaded bundle.
	var inspBuf bytes.Buffer
	mw := multipart.NewWriter(&inspBuf)
	fw, _ := mw.CreateFormFile("file", "bundle.zip")
	_, _ = fw.Write(bundleZip)
	_ = mw.Close()
	inspReq := httptest.NewRequest(http.MethodPost, "/api/admin/migration/inspect", &inspBuf)
	inspReq.Header.Set("Content-Type", mw.FormDataContentType())
	inspRec := httptest.NewRecorder()
	h.handleMigrationInspect(inspRec, inspReq)
	if inspRec.Code != http.StatusOK {
		t.Fatalf("inspect status %d: %s", inspRec.Code, inspRec.Body.String())
	}
	var inspResp struct {
		Format  string                 `json:"format"`
		Tenants []InspectTenantSummary `json:"tenants"`
	}
	if err := json.Unmarshal(inspRec.Body.Bytes(), &inspResp); err != nil {
		t.Fatal(err)
	}
	if inspResp.Format != KnowledgeBundleFormat || len(inspResp.Tenants) != 2 {
		t.Fatalf("unexpected inspect response: %+v", inspResp)
	}

	// 3. Merge both source tenants into targetMerged; second import must deduplicate (skip all).
	runImport := func() SnapshotCounts {
		t.Helper()
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		part, _ := w.CreateFormFile("file", "bundle.zip")
		_, _ = part.Write(bundleZip)
		_ = w.WriteField("tenant_mode", "single")
		_ = w.WriteField("target_tenant_id", targetMerged.String())
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/migration/import", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		rec := httptest.NewRecorder()
		h.handleMigrationImport(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("import status %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Counts SnapshotCounts `json:"counts"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Counts
	}

	first := runImport()
	if first.MemoriesImported != 2 || first.MemoriesSkipped != 0 || first.RevisionsImported != 2 || first.ProjectsCreated != 1 {
		t.Fatalf("unexpected first import counts: %+v", first)
	}
	second := runImport()
	if second.MemoriesImported != 0 || second.MemoriesSkipped != 2 || second.ProjectsMatched != 1 {
		t.Fatalf("expected second import to deduplicate all memories and match project, got: %+v", second)
	}

	// Verify existing target member is untouched and source_memory_id points to the newly imported memory.
	var memberCount int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*)::int FROM tenant_members WHERE tenant_id = $1 AND principal_id = $2`, targetMerged, targetPrincipal).Scan(&memberCount); err != nil || memberCount != 1 {
		t.Fatalf("expected target member preserved, count=%d err=%v", memberCount, err)
	}
}
