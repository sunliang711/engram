package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"engram/internal/config"
	"engram/internal/db"
	"github.com/google/uuid"
)

// TestLegacyPermissionInputs rejects removed roles before a database write can occur.
func TestLegacyPermissionInputs(t *testing.T) {
	handler := &Handler{}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		body    map[string]string
	}{
		{"member admin", handler.handlePrincipals, map[string]string{"tenant_id": uuid.NewString(), "subject": "legacy", "display_name": "Legacy", "tenant_role": "admin"}},
		{"quickstart admin", handler.handleQuickstart, map[string]string{"tenant_name": "Legacy", "principal_subject": "legacy", "principal_display_name": "Legacy", "tenant_role": "admin"}},
		{"project maintainer", handler.handleProjectMembers, map[string]string{"project_id": uuid.NewString(), "principal_id": uuid.NewString(), "role": "maintainer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			tc.handler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestScopedGrantAdministration verifies safe defaults and grant revocation through the real handlers.
func TestScopedGrantAdministration(t *testing.T) {
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
	tenant, project := uuid.New(), uuid.New()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Permission test')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO projects(id,tenant_id,name) VALUES($1,$2,'Permission test')`, project, tenant); err != nil {
		t.Fatal(err)
	}
	var principal uuid.UUID
	defer func() {
		for _, statement := range []string{
			`DELETE FROM project_members WHERE project_id=$1`,
			`DELETE FROM projects WHERE id=$1`,
		} {
			if _, err := database.Pool.Exec(ctx, statement, project); err != nil {
				t.Errorf("cleanup project: %v", err)
			}
		}
		if _, err := database.Pool.Exec(ctx, `DELETE FROM tenant_members WHERE tenant_id=$1`, tenant); err != nil {
			t.Errorf("cleanup membership: %v", err)
		}
		if _, err := database.Pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenant); err != nil {
			t.Errorf("cleanup tenant: %v", err)
		}
		if _, err := database.Pool.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principal); err != nil {
			t.Errorf("cleanup principal: %v", err)
		}
	}()
	handler := &Handler{pool: database.Pool}
	body, err := json.Marshal(map[string]string{"tenant_id": tenant.String(), "subject": "permissions-" + project.String(), "display_name": "Scoped"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.handlePrincipals(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create member: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		PrincipalID uuid.UUID `json:"principal_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	principal = result.PrincipalID
	var role string
	if err := database.Pool.QueryRow(ctx, `SELECT role FROM tenant_members WHERE tenant_id=$1 AND principal_id=$2`, tenant, principal).Scan(&role); err != nil || role != "none" {
		t.Fatalf("default role=%s err=%v", role, err)
	}
	for _, grant := range []string{"reader", "writer", "none", "none"} {
		body, err := json.Marshal(map[string]string{"project_id": project.String(), "principal_id": principal.String(), "role": grant})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		handler.handleProjectMembers(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("grant %s: %d %s", grant, rec.Code, rec.Body.String())
		}
		var count int
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM project_members WHERE project_id=$1 AND principal_id=$2`, project, principal).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if grant == "none" && count != 0 {
			t.Fatal("revoked membership still exists")
		}
		if grant != "none" {
			if err := database.Pool.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id=$1 AND principal_id=$2`, project, principal).Scan(&role); err != nil || role != grant {
				t.Fatalf("role=%s want=%s err=%v", role, grant, err)
			}
		}
	}
}
