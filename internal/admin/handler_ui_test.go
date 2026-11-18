package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"

	"engram/internal/config"
	"engram/internal/db"

	"github.com/google/uuid"
)

// TestParseMemoryPage covers bounds before invalid requests can reach PostgreSQL.
func TestParseMemoryPage(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		page, size  int
		invalid     bool
	}{
		{"defaults", "", 1, 20, false},
		{"last bounds", "page=1000000&page_size=100", 1000000, 100, false},
		{"zero page", "page=0", 0, 0, true},
		{"negative size", "page_size=-1", 0, 0, true},
		{"large size", "page_size=101", 0, 0, true},
		{"large page", "page=1000001", 0, 0, true},
		{"overflow", "page=9999999999999999999999", 0, 0, true},
		{"fraction", "page=1.5", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			page, size, err := parseMemoryPage(q)
			if (err != nil) != tc.invalid || page != tc.page || size != tc.size {
				t.Fatalf("got page=%d size=%d err=%v", page, size, err)
			}
		})
	}
}

// TestAdminUIRegression uses an explicitly configured test database and only its own fixtures.
func TestAdminUIRegression(t *testing.T) {
	dsn := os.Getenv("ENGRAM_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_ADMIN_TEST_DSN is required for the admin PostgreSQL regression test")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tenant, otherTenant, principal := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{tenant, otherTenant} {
		if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants(id, name) VALUES($1, 'ui-regression')`, id); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		// These fixtures have no revisions, events, or API tokens referencing them.
		for _, statement := range []string{
			`DELETE FROM memories WHERE tenant_id = $1 OR tenant_id = $2`,
			`DELETE FROM tenant_members WHERE tenant_id = $1 OR tenant_id = $2`,
			`DELETE FROM tenants WHERE id = $1 OR id = $2`,
		} {
			if _, err := database.Pool.Exec(ctx, statement, tenant, otherTenant); err != nil {
				t.Errorf("cleanup fixture: %v", err)
			}
		}
		if _, err := database.Pool.Exec(ctx, `DELETE FROM principals WHERE id = $1`, principal); err != nil {
			t.Errorf("cleanup principal: %v", err)
		}
	}()
	subject := "ui-regression-" + principal.String()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO principals(id, identity_provider, subject, display_name) VALUES($1,'api_token',$2,'Original Name')`, principal, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,'admin')`, tenant, principal); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO memories(id,tenant_id,scope,category,title,content_md,content_hash,created_by,updated_by,deleted_at)
		SELECT gen_random_uuid(),$1,'global','testing','fixture ' || n,'body','fixture',$2,$2,
		       CASE WHEN n % 20 = 0 THEN now() END
		FROM generate_series(1,106) n
	`, tenant, principal); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{pool: database.Pool}
	// list invokes the real SQL handler with tenant-scoped pagination and decodes its contract.
	list := func(query string) struct {
		Items []struct{ ID string } `json:"items"`
		Total int                   `json:"total"`
		Page  int                   `json:"page"`
	} {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.handleMemories(rec, httptest.NewRequest(http.MethodGet, "/?tenant_id="+tenant.String()+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
		}
		var result struct {
			Items []struct{ ID string } `json:"items"`
			Total int                   `json:"total"`
			Page  int                   `json:"page"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("all 106 records remain reachable even with equal timestamps", func(t *testing.T) {
		seen := make(map[string]bool)
		for page := 1; page <= 6; page++ {
			result := list("&page=" + strconv.Itoa(page))
			if result.Total != 106 || result.Page != page {
				t.Fatalf("unexpected total/page: %+v", result)
			}
			for _, item := range result.Items {
				if seen[item.ID] {
					t.Fatalf("duplicate across pages: %s", item.ID)
				}
				seen[item.ID] = true
			}
		}
		if len(seen) != 106 {
			t.Fatalf("reached %d records, want 106", len(seen))
		}
	})
	t.Run("filtered totals empty results and stale pages", func(t *testing.T) {
		if got := list("&deleted=deleted"); got.Total != 5 || len(got.Items) != 5 {
			t.Fatalf("deleted result: %+v", got)
		}
		if got := list("&q=does-not-exist"); got.Total != 0 || len(got.Items) != 0 || got.Page != 1 {
			t.Fatalf("empty result: %+v", got)
		}
		if got := list("&page=999"); got.Page != 6 || len(got.Items) != 6 {
			t.Fatalf("clamped result: %+v", got)
		}
		first := list("").Items[0].ID
		if got := list("&memory_id=" + first); got.Total != 1 || len(got.Items) != 1 || got.Items[0].ID != first {
			t.Fatalf("deep-link lookup: %+v", got)
		}
	})
	t.Run("category and tag filters with pinned entries first", func(t *testing.T) {
		if _, err := database.Pool.Exec(ctx, `UPDATE memories SET category = 'runbook', tags = ARRAY['ops'] WHERE tenant_id = $1 AND title IN ('fixture 1', 'fixture 2')`, tenant); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Pool.Exec(ctx, `UPDATE memories SET is_pinned = true, updated_at = now() - interval '1 day' WHERE tenant_id = $1 AND title = 'fixture 3'`, tenant); err != nil {
			t.Fatal(err)
		}
		if got := list("&category=runbook"); got.Total != 2 {
			t.Fatalf("category filter: %+v", got)
		}
		if got := list("&tag=ops&category=runbook"); got.Total != 2 {
			t.Fatalf("tag filter: %+v", got)
		}
		if got := list("&tag=missing"); got.Total != 0 {
			t.Fatalf("missing tag: %+v", got)
		}
		var pinnedID string
		if err := database.Pool.QueryRow(ctx, `SELECT id::text FROM memories WHERE tenant_id = $1 AND title = 'fixture 3'`, tenant).Scan(&pinnedID); err != nil {
			t.Fatal(err)
		}
		if first := list("").Items[0].ID; first != pinnedID {
			t.Fatalf("pinned entry should lead the list, got %s", first)
		}
		if first := list("&sort=recent").Items[0].ID; first == pinnedID {
			t.Fatal("sort=recent must ignore pinning")
		}
	})
	t.Run("project names stay unique within a tenant", func(t *testing.T) {
		defer func() {
			if _, err := database.Pool.Exec(ctx, `DELETE FROM projects WHERE tenant_id = $1`, tenant); err != nil {
				t.Errorf("cleanup projects: %v", err)
			}
		}()
		create := func(status int) {
			t.Helper()
			payload, _ := json.Marshal(map[string]any{"tenant_id": tenant.String(), "name": "ui-regression-project", "with_marker": true})
			rec := httptest.NewRecorder()
			handler.handleProjects(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)))
			if rec.Code != status {
				t.Fatalf("project status %d, want %d: %s", rec.Code, status, rec.Body.String())
			}
		}
		create(http.StatusCreated)
		create(http.StatusConflict)
	})
	// postPrincipal exercises creation and binding semantics without bypassing handler validation.
	postPrincipal := func(body map[string]string, status int) {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		handler.handlePrincipals(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)))
		if rec.Code != status {
			t.Fatalf("principal status %d, want %d: %s", rec.Code, status, rec.Body.String())
		}
	}
	t.Run("duplicate creation cannot rename or alter permissions", func(t *testing.T) {
		postPrincipal(map[string]string{"tenant_id": tenant.String(), "subject": subject, "display_name": "Overwritten", "tenant_role": "member"}, http.StatusConflict)
		var name, role string
		if err := database.Pool.QueryRow(ctx, `SELECT p.display_name, m.role FROM principals p JOIN tenant_members m ON m.principal_id=p.id WHERE p.id=$1 AND m.tenant_id=$2`, principal, tenant).Scan(&name, &role); err != nil {
			t.Fatal(err)
		}
		if name != "Original Name" || role != "admin" {
			t.Fatalf("unexpected overwrite: name=%s role=%s", name, role)
		}
	})
	t.Run("binding preserves existing role and explicit role changes stay tenant scoped", func(t *testing.T) {
		postPrincipal(map[string]string{"mode": "bind", "tenant_id": otherTenant.String(), "principal_id": principal.String(), "tenant_role": "member"}, http.StatusCreated)
		postPrincipal(map[string]string{"mode": "bind", "tenant_id": tenant.String(), "principal_id": principal.String(), "tenant_role": "member"}, http.StatusConflict)
		postPrincipal(map[string]string{"mode": "update_role", "tenant_id": otherTenant.String(), "principal_id": principal.String(), "tenant_role": "global_writer"}, http.StatusOK)
		for id, expected := range map[uuid.UUID]string{tenant: "admin", otherTenant: "global_writer"} {
			var role string
			if err := database.Pool.QueryRow(ctx, `SELECT role FROM tenant_members WHERE tenant_id=$1 AND principal_id=$2`, id, principal).Scan(&role); err != nil {
				t.Fatal(err)
			}
			if role != expected {
				t.Fatalf("role=%s want=%s", role, expected)
			}
		}
	})
}
