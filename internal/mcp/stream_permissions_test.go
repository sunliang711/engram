package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"engram/internal/auth"
	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/notify"
	"engram/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// TestStreamClosesWhenReadPermissionRevoked checks both notification-time and keepalive-time authorization.
func TestStreamClosesWhenReadPermissionRevoked(t *testing.T) {
	dsn := os.Getenv("ENGRAM_STREAM_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_STREAM_TEST_DSN is required")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authenticator, err := auth.NewAuthenticator(database.Pool, cfg.Auth)
	if err != nil {
		t.Fatal(err)
	}
	signer := auth.NewTokenSigner("stream-permission-test-signing-key", time.Hour)
	for _, tc := range []struct {
		name       string
		project    bool
		keepalive  bool
		credential string
	}{
		{"project grant removed before notification", true, false, ""},
		{"global grant removed before notification", false, false, ""},
		{"project grant removed without further notifications", true, true, ""},
		{"token revoked with member grants retained", true, false, "revoked"},
		{"token expired with member grants retained", true, false, "expired"},
		{"token rebound to another tenant with global access", false, false, "tenant_rebound"},
		{"token rebound to another principal with global access", false, false, "principal_rebound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenant, principal, project := uuid.New(), uuid.New(), uuid.New()
			if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Stream test')`, tenant); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, statement := range []string{
					`DELETE FROM api_tokens WHERE tenant_id=$1`,
					`DELETE FROM project_members WHERE project_id IN (SELECT id FROM projects WHERE tenant_id=$1)`,
					`DELETE FROM projects WHERE tenant_id=$1`,
					`DELETE FROM tenant_members WHERE tenant_id=$1`,
					`DELETE FROM tenants WHERE id=$1`,
				} {
					if _, err := database.Pool.Exec(ctx, statement, tenant); err != nil {
						t.Errorf("cleanup stream fixture: %v", err)
					}
				}
				if _, err := database.Pool.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principal); err != nil {
					t.Errorf("cleanup stream principal: %v", err)
				}
			})
			if _, err := database.Pool.Exec(ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,'api_token',$2,'Stream member')`, principal, principal.String()); err != nil {
				t.Fatal(err)
			}
			role := "member"
			if tc.project {
				role = "none"
			}
			if _, err := database.Pool.Exec(ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,$3)`, tenant, principal, role); err != nil {
				t.Fatal(err)
			}
			var projectID *uuid.UUID
			mode := auth.ModeGlobalOnly
			if tc.project {
				projectID = &project
				mode = auth.ModeProject
				if _, err := database.Pool.Exec(ctx, `INSERT INTO projects(id,tenant_id,name) VALUES($1,$2,'Stream project')`, project, tenant); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Pool.Exec(ctx, `INSERT INTO project_members(project_id,principal_id,role) VALUES($1,$2,'reader')`, project, principal); err != nil {
					t.Fatal(err)
				}
			}
			rawToken := "stream-" + uuid.NewString()
			if _, err := database.Pool.Exec(ctx, `INSERT INTO api_tokens(id,tenant_id,principal_id,token_hash) VALUES($1,$2,$3,$4)`, uuid.New(), tenant, principal, auth.HashToken(rawToken)); err != nil {
				t.Fatal(err)
			}
			projectContext, err := signer.SignProjectContext(tenant, projectID, principal, mode)
			if err != nil {
				t.Fatal(err)
			}
			hub := notify.NewHub(database.Pool, signer, zerolog.Nop())
			server := NewServer(database, authenticator, signer, nil, hub, observability.NewMetrics(), zerolog.Nop())
			httpServer := httptest.NewServer(server.Handler())
			defer httpServer.Close()
			requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, httpServer.URL+"/mcp?project_context="+url.QueryEscape(projectContext), nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+rawToken)
			request.Header.Set("Accept", "text/event-stream")
			response, err := httpServer.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("stream status=%d", response.StatusCode)
			}

			// A received notice proves subscription is ready before revoking the grant.
			publishCtx, stopPublishing := context.WithCancel(requestCtx)
			defer stopPublishing()
			publishDone := make(chan struct{})
			go func() {
				defer close(publishDone)
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-publishCtx.Done():
						return
					case <-ticker.C:
						hub.PublishLocal(notify.EventPayload{TenantID: tenant, ProjectID: projectID, EventRevision: 1})
					}
				}
			}()
			reader := bufio.NewReader(response.Body)
			readNotice := func() int64 {
				t.Helper()
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatalf("initial stream notice: %v", err)
					}
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var notice struct {
						Params notify.ClientNotice `json:"params"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &notice); err != nil {
						t.Fatal(err)
					}
					if line, err := reader.ReadString('\n'); err != nil || line != "\n" {
						t.Fatalf("incomplete stream notice: %q, %v", line, err)
					}
					revision, err := signer.VerifyCursor(notice.Params.LatestCursor, tenant, projectID)
					if err != nil {
						t.Fatal(err)
					}
					return revision
				}
			}
			readNotice()
			stopPublishing()
			<-publishDone
			// Drain every setup notice before revocation so only the tested trigger can close the stream.
			hub.PublishLocal(notify.EventPayload{TenantID: tenant, ProjectID: projectID, EventRevision: 2})
			for readNotice() != 2 {
			}

			if tc.credential == "tenant_rebound" || tc.credential == "principal_rebound" {
				// Keep the replacement identity authorized so only identity binding can close the old stream.
				replacementTenant, replacementPrincipal := tenant, principal
				if tc.credential == "tenant_rebound" {
					replacementTenant = uuid.New()
					if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Replacement tenant')`, replacementTenant); err != nil {
						t.Fatal(err)
					}
				} else {
					replacementPrincipal = uuid.New()
					if _, err := database.Pool.Exec(ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,'api_token',$2,'Replacement member')`, replacementPrincipal, replacementPrincipal.String()); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() {
					for _, statement := range []string{
						`DELETE FROM api_tokens WHERE tenant_id=$1 AND principal_id=$2`,
						`DELETE FROM tenant_members WHERE tenant_id=$1 AND principal_id=$2`,
					} {
						if _, err := database.Pool.Exec(ctx, statement, replacementTenant, replacementPrincipal); err != nil {
							t.Errorf("cleanup replacement identity: %v", err)
						}
					}
					if replacementTenant != tenant {
						if _, err := database.Pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, replacementTenant); err != nil {
							t.Errorf("cleanup replacement tenant: %v", err)
						}
					}
					if replacementPrincipal != principal {
						if _, err := database.Pool.Exec(ctx, `DELETE FROM principals WHERE id=$1`, replacementPrincipal); err != nil {
							t.Errorf("cleanup replacement principal: %v", err)
						}
					}
				})
				if _, err := database.Pool.Exec(ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,'member')`, replacementTenant, replacementPrincipal); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Pool.Exec(ctx, `UPDATE api_tokens SET tenant_id=$1,principal_id=$2 WHERE token_hash=$3`, replacementTenant, replacementPrincipal, auth.HashToken(rawToken)); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Pool.Exec(ctx, `UPDATE tenant_members SET role='none' WHERE tenant_id=$1 AND principal_id=$2`, tenant, principal); err != nil {
					t.Fatal(err)
				}
			} else if tc.credential != "" {
				statement := `UPDATE api_tokens SET revoked_at=now() WHERE tenant_id=$1 AND principal_id=$2`
				if tc.credential == "expired" {
					statement = `UPDATE api_tokens SET expires_at=now() - interval '1 second' WHERE tenant_id=$1 AND principal_id=$2`
				}
				if _, err := database.Pool.Exec(ctx, statement, tenant, principal); err != nil {
					t.Fatal(err)
				}
			} else if tc.project {
				if _, err := database.Pool.Exec(ctx, `DELETE FROM project_members WHERE project_id=$1 AND principal_id=$2`, project, principal); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := database.Pool.Exec(ctx, `UPDATE tenant_members SET role='none' WHERE tenant_id=$1 AND principal_id=$2`, tenant, principal); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.keepalive {
				hub.PublishLocal(notify.EventPayload{TenantID: tenant, ProjectID: projectID, EventRevision: 3})
			}
			remaining, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("revoked stream did not close cleanly: %v", err)
			}
			if strings.Contains(string(remaining), "data: ") || strings.Contains(string(remaining), ": keepalive") {
				t.Fatalf("revoked stream sent additional frames: %s", remaining)
			}
		})
	}
}
