package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/project"

	"github.com/google/uuid"
)

// TestAdminRegistrationLock serializes all administrator identity writers with MCP registration.
func TestAdminRegistrationLock(t *testing.T) {
	dsn := os.Getenv("ENGRAM_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_ADMIN_TEST_DSN is required for the registration lock test")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tenantID, principalID, projectID := uuid.New(), uuid.New(), uuid.New()
	tenantName, subject := "lock-"+tenantID.String(), "lock-"+principalID.String()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,$2)`, tenantID, tenantName); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, query := range []string{
			`DELETE FROM api_tokens WHERE tenant_id=$1`,
			`DELETE FROM project_members WHERE project_id IN (SELECT id FROM projects WHERE tenant_id=$1)`,
			`DELETE FROM project_repositories WHERE tenant_id=$1`,
			`DELETE FROM projects WHERE tenant_id=$1`,
			`DELETE FROM tenant_members WHERE tenant_id=$1`,
			`DELETE FROM tenants WHERE id=$1`,
		} {
			if _, err := database.Pool.Exec(ctx, query, tenantID); err != nil {
				t.Errorf("cleanup registration fixtures: %v", err)
			}
		}
		if _, err := database.Pool.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principalID); err != nil {
			t.Errorf("cleanup principal: %v", err)
		}
	}()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,'api_token',$2,'Original')`, principalID, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,'none')`, tenantID, principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO projects(id,tenant_id,name) VALUES($1,$2,'Existing')`, projectID, tenantID); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{pool: database.Pool, authMgr: NewAuthManager(database.Pool, "test-registration-signing-key-at-least-32-bytes", time.Hour, false)}
	sourceTenantID, sourceProjectID := uuid.New(), uuid.New()
	bundle := KnowledgeBundlePayload{
		Tenants:             []snapshotTenant{{ID: sourceTenantID, Name: "Source", Status: "active"}},
		Projects:            []snapshotProject{{ID: sourceProjectID, TenantID: sourceTenantID, Name: "Imported", Status: "active"}},
		ProjectRepositories: []snapshotProjectRepository{{ID: uuid.New(), TenantID: sourceTenantID, ProjectID: sourceProjectID, CanonicalRemote: "github.com/example/" + sourceProjectID.String()}},
	}
	// importBundle exercises the import transaction with a project and repository identity to write.
	importBundle := func(w http.ResponseWriter, r *http.Request) {
		counts, err := handler.importKnowledgeBundlePayload(r.Context(), bundle, KnowledgeImportOptions{TenantMode: "single", TargetTenantID: tenantID})
		if err != nil {
			writeJSONError(w, err)
			return
		}
		if counts.ProjectsCreated != 1 || counts.ProjectRepositories != 1 {
			http.Error(w, "knowledge bundle did not create the expected identity", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, counts)
	}
	for _, tc := range []struct {
		name   string
		handle http.HandlerFunc
		body   map[string]any
		status int
	}{
		{"quickstart", handler.handleQuickstart, map[string]any{"tenant_name": tenantName, "principal_subject": subject, "principal_display_name": "Updated", "project_name": "Quickstart"}, http.StatusOK},
		{"projects", handler.handleProjects, map[string]any{"tenant_id": tenantID, "name": "Registered"}, http.StatusCreated},
		{"remotes", handler.handleProjectRemotes, map[string]any{"project_id": projectID, "remote_url": "https://github.com/example/" + projectID.String()}, http.StatusCreated},
		{"knowledge bundle", importBundle, map[string]any{}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock, err := database.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Rollback(ctx) }()
			if err := project.LockRegistration(ctx, lock, tenantID); err != nil {
				t.Fatal(err)
			}
			var blockerPID int32
			if err := lock.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				tc.handle(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(requestCtx))
				close(done)
			}()

			// Observe the actual database wait instead of assuming scheduling from elapsed time.
			deadline := time.After(5 * time.Second)
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
		waiting:
			for {
				var blocked bool
				if err := database.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE NOT granted AND locktype='advisory' AND $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break waiting
				}
				select {
				case <-done:
					t.Fatalf("handler bypassed registration lock: %d %s", rec.Code, rec.Body.String())
				case <-deadline:
					t.Fatal("handler did not wait for registration lock")
				case <-ticker.C:
				}
			}
			if tc.name == "quickstart" {
				var displayName string
				if err := database.Pool.QueryRow(ctx, `SELECT display_name FROM principals WHERE id=$1`, principalID).Scan(&displayName); err != nil {
					t.Fatal(err)
				}
				if displayName != "Original" {
					t.Fatal("quickstart updated the principal before acquiring registration lock")
				}
			}
			if err := lock.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
				if rec.Code != tc.status {
					t.Fatalf("handler after lock release: %d %s", rec.Code, rec.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not finish after registration lock release")
			}
		})
	}
	t.Run("quickstart preserves existing creation permission when omitted", func(t *testing.T) {
		if _, err := database.Pool.Exec(ctx, `UPDATE tenant_members SET can_create_project=true WHERE tenant_id=$1 AND principal_id=$2`, tenantID, principalID); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			permission *bool
			want       bool
		}{
			{nil, true},
			{new(bool), false},
			{nil, false},
		} {
			body := map[string]any{"tenant_name": tenantName, "principal_subject": subject, "principal_display_name": "Updated", "tenant_role": "none"}
			if tc.permission != nil {
				body["can_create_project"] = *tc.permission
			}
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			handler.handleQuickstart(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)))
			if rec.Code != http.StatusOK {
				t.Fatalf("reuse member status=%d body=%s", rec.Code, rec.Body.String())
			}
			var allowed bool
			if err := database.Pool.QueryRow(ctx, `SELECT can_create_project FROM tenant_members WHERE tenant_id=$1 AND principal_id=$2`, tenantID, principalID).Scan(&allowed); err != nil || allowed != tc.want {
				t.Fatalf("reused member permission=%v want=%v err=%v", allowed, tc.want, err)
			}
		}
	})
}
