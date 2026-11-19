package memory_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"engram/internal/apperror"
	"engram/internal/auth"
	"engram/internal/memory"
	"engram/internal/project"

	"github.com/google/uuid"
)

// TestProjectRegistration covers independent grants, replay safety, identity protection, and isolation.
func TestProjectRegistration(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()
	tenant, user, token, ctx := env.createTenantAndPrincipal(t, "Registration", "creator", "global_writer")
	req := memory.RegisterProjectRequest{Name: "repo", Remotes: []project.RemoteInput{{Name: "origin", URL: "git@github.com:team/repo.git"}}, IdempotencyKey: "register"}
	_, err := env.svc.RegisterProject(ctx, req)
	assertAppErrorCode(t, err, apperror.CodeUnauthorized)
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET can_create_project=true,role='none' WHERE tenant_id=$1 AND principal_id=$2`, tenant, user); err != nil {
		t.Fatal(err)
	}
	global, err := env.svc.Discover(ctx, memory.DiscoverRequest{AllowGlobalOnly: true})
	if err != nil || !global.Permissions.CanCreateProject || global.Permissions.CanWriteGlobal {
		t.Fatalf("independent creation grant: %+v, %v", global, err)
	}
	_, err = env.svc.Discover(ctx, memory.DiscoverRequest{Remotes: req.Remotes})
	assertAppErrorCode(t, err, apperror.CodeProjectNotRegistered)
	var discoverErr *apperror.Error
	if !errors.As(err, &discoverErr) || discoverErr.Details["can_create_project"] != true {
		t.Fatalf("missing creation permission hint: %v", err)
	}
	registered, err := env.svc.RegisterProject(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	disc, err := env.svc.Discover(ctx, memory.DiscoverRequest{Remotes: req.Remotes})
	if err != nil || disc.Project.ID != registered.Project.ID || !disc.Permissions.CanWriteProject || disc.Permissions.CanWriteGlobal {
		t.Fatalf("registered project discovery: %+v, %v", disc, err)
	}
	if _, err := env.svc.Create(ctx, memory.CreateRequest{ProjectContext: disc.ProjectContext, Scope: "project", Category: "testing", Title: "Registered", Content: "body", IdempotencyKey: "first-memory"}, "registration"); err != nil {
		t.Fatal(err)
	}
	replay, err := env.svc.RegisterProject(ctx, req)
	if err != nil || replay.Project.ID != registered.Project.ID {
		t.Fatalf("idempotent registration: %+v, %v", replay, err)
	}
	changed := req
	changed.Name = "different"
	_, err = env.svc.RegisterProject(ctx, changed)
	assertAppErrorCode(t, err, apperror.CodeIdempotencyConflict)

	t.Run("marker-only and normalized aliases", func(t *testing.T) {
		markerReq := memory.RegisterProjectRequest{Name: "marker-only", ProjectMarker: uuid.NewString(), WorkspacePath: "tools/local", IdempotencyKey: "marker-only"}
		_, err := env.svc.RegisterProject(ctx, markerReq)
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)
		markerReq.WorkspacePath = ""
		markerProject, err := env.svc.RegisterProject(ctx, markerReq)
		if err != nil {
			t.Fatal(err)
		}
		if markerProject.Project.WorkspacePath != "" {
			t.Fatalf("marker-only registration returned a persistent workspace: %+v", markerProject)
		}
		markerDisc, err := env.svc.Discover(ctx, memory.DiscoverRequest{ProjectMarker: markerReq.ProjectMarker, MarkerPath: "tools/local", WorkspacePath: "tools/local"})
		if err != nil || markerDisc.Project.ID != markerProject.Project.ID {
			t.Fatalf("marker discovery: %+v, %v", markerDisc, err)
		}
		aliasReq := memory.RegisterProjectRequest{Name: "aliases", Remotes: []project.RemoteInput{{Name: "origin", URL: "https://github.com/team/aliases.git"}, {Name: "mirror", URL: "git@github.com:team/aliases.git"}}, IdempotencyKey: "aliases"}
		aliases, err := env.svc.RegisterProject(ctx, aliasReq)
		if err != nil {
			t.Fatal(err)
		}
		aliasReq.Remotes = []project.RemoteInput{{Name: "renamed", URL: "ssh://git@github.com/team/aliases"}}
		replayed, err := env.svc.RegisterProject(ctx, aliasReq)
		if err != nil || replayed.Project.ID != aliases.Project.ID {
			t.Fatalf("normalized alias replay: %+v, %v", replayed, err)
		}
	})

	// Another member cannot acquire writer access through a duplicate registration.
	_, other, _, otherCtx := env.createTenantAndPrincipal(t, "Unused", "other-creator", "none")
	if _, err := env.database.Pool.Exec(env.ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role,can_create_project) VALUES($1,$2,'none',true)`, tenant, other); err != nil {
		t.Fatal(err)
	}
	// Use the existing helper's context identity with the target tenant's membership.
	otherCtx = auth.WithPrincipal(env.ctx, auth.Principal{TenantID: tenant, PrincipalID: other})
	duplicate := req
	duplicate.IdempotencyKey = "duplicate"
	duplicate.Name = "another-name"
	_, err = env.svc.RegisterProject(otherCtx, duplicate)
	assertAppErrorCode(t, err, apperror.CodeProjectExists)
	var grants int
	if err := env.database.Pool.QueryRow(env.ctx, `SELECT count(*) FROM project_members WHERE project_id=$1 AND principal_id=$2`, registered.Project.ID, other).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("duplicate granted access: %d, %v", grants, err)
	}

	t.Run("validation", func(t *testing.T) {
		for _, input := range []memory.RegisterProjectRequest{
			{Name: "", Remotes: req.Remotes, IdempotencyKey: "empty-name"},
			{Name: "no-identity", IdempotencyKey: "empty-identity"},
			{Name: "bad-path", Remotes: req.Remotes, WorkspacePath: "/tmp/repo", IdempotencyKey: "bad-path"},
			{Name: "bad-marker", ProjectMarker: "bad", IdempotencyKey: "bad-marker"},
			{Name: "zero-marker", ProjectMarker: uuid.Nil.String(), IdempotencyKey: "zero-marker"},
			{Name: "bad-remote", Remotes: []project.RemoteInput{{URL: "bad"}}, IdempotencyKey: "bad-remote"},
			{Name: "missing-key", Remotes: req.Remotes},
		} {
			_, err := env.svc.RegisterProject(ctx, input)
			assertAppErrorCode(t, err, apperror.CodeInvalidArgument)
		}
	})

	// MCP dispatch rejects caller-selected tenant identities and supports the full registration tool.
	mcpReq := memory.RegisterProjectRequest{Name: "from-mcp", ProjectMarker: uuid.NewString(), IdempotencyKey: "mcp"}
	payload, _ := json.Marshal(mcpReq)
	var args map[string]any
	if err := json.Unmarshal(payload, &args); err != nil {
		t.Fatal(err)
	}
	result := callMCPTool(t, env.mcpServer.URL, token, "project_register", args)
	if result["isError"] == true {
		t.Fatalf("MCP registration: %+v", result)
	}
	args["tenant_id"] = uuid.NewString()
	result = callMCPTool(t, env.mcpServer.URL, token, "project_register", args)
	if result["isError"] != true {
		t.Fatal("MCP accepted caller-selected tenant")
	}

	// Revocation applies to both old and new registration requests without changing the token.
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET can_create_project=false WHERE tenant_id=$1 AND principal_id=$2`, tenant, user); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.RegisterProject(ctx, req)
	assertAppErrorCode(t, err, apperror.CodeUnauthorized)
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET can_create_project=true WHERE tenant_id=$1 AND principal_id=$2`, tenant, user); err != nil {
		t.Fatal(err)
	}
	env.addProjectMember(t, registered.Project.ID, user, "reader")
	_, err = env.svc.RegisterProject(ctx, req)
	assertAppErrorCode(t, err, apperror.CodeUnauthorized)

	// Repository identities are tenant-scoped and disabled mappings remain reserved.
	otherTenant, _, _, isolatedCtx := env.createTenantAndPrincipal(t, "Isolated", "isolated", "none")
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET can_create_project=true WHERE tenant_id=$1`, otherTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.RegisterProject(isolatedCtx, req); err != nil {
		t.Fatalf("cross-tenant registration: %v", err)
	}
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE projects SET status='disabled' WHERE id=$1`, registered.Project.ID); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.RegisterProject(ctx, duplicate)
	assertAppErrorCode(t, err, apperror.CodeProjectExists)
}

// TestProjectRegistrationLockTimeout verifies retryable contention and rollback before a same-key retry.
func TestProjectRegistrationLockTimeout(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()
	tenant, user, _, ctx := env.createTenantAndPrincipal(t, "Busy", "creator", "none")
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET can_create_project=true WHERE tenant_id=$1 AND principal_id=$2`, tenant, user); err != nil {
		t.Fatal(err)
	}
	blocker, err := env.database.Pool.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(env.ctx) }()
	if err := project.LockRegistration(env.ctx, blocker, tenant); err != nil {
		t.Fatal(err)
	}
	req := memory.RegisterProjectRequest{Name: "blocked", ProjectMarker: uuid.NewString(), IdempotencyKey: "retry-after-lock"}
	_, err = env.svc.RegisterProject(ctx, req)
	assertAppErrorCode(t, err, apperror.CodeLockTimeout)
	var appErr *apperror.Error
	if !errors.As(err, &appErr) || !appErr.Retryable || appErr.HTTPStatus() != http.StatusServiceUnavailable {
		t.Fatalf("contention should be retryable HTTP 503: %v", err)
	}
	var projects, requests int
	if err := env.database.Pool.QueryRow(env.ctx, `SELECT (SELECT count(*) FROM projects WHERE tenant_id=$1), (SELECT count(*) FROM idempotency_requests WHERE tenant_id=$1)`, tenant).Scan(&projects, &requests); err != nil || projects != 0 || requests != 0 {
		t.Fatalf("timed out registration left partial writes: projects=%d requests=%d err=%v", projects, requests, err)
	}
	if err := blocker.Commit(env.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.RegisterProject(ctx, req); err != nil {
		t.Fatalf("same-key retry after contention: %v", err)
	}
}

// TestConcurrentProjectRegistration ensures concurrent creators cannot produce duplicate or shadowing mappings.
func TestConcurrentProjectRegistration(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()
	tenant, _, _, ctx := env.createTenantAndPrincipal(t, "Concurrent", "creator", "none")
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET can_create_project=true WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := env.svc.RegisterProject(ctx, memory.RegisterProjectRequest{Name: name, Remotes: []project.RemoteInput{{URL: "https://github.com/team/concurrent"}}, WorkspacePath: "services/api", IdempotencyKey: name})
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			assertAppErrorCode(t, err, apperror.CodeProjectExists)
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	for _, path := range []string{"", "services", "services/api/v1"} {
		_, err := env.svc.RegisterProject(ctx, memory.RegisterProjectRequest{Name: "shadow", Remotes: []project.RemoteInput{{URL: "git@github.com:team/concurrent.git"}}, WorkspacePath: path, IdempotencyKey: "shadow-" + path})
		assertAppErrorCode(t, err, apperror.CodeProjectExists)
	}
	if _, err := env.svc.RegisterProject(ctx, memory.RegisterProjectRequest{Name: "sibling", Remotes: []project.RemoteInput{{URL: "https://github.com/team/concurrent"}}, WorkspacePath: "services/web", IdempotencyKey: "sibling"}); err != nil {
		t.Fatalf("disjoint monorepo workspace: %v", err)
	}
}

// callMCPTool exercises argument validation and dispatch through the authenticated HTTP transport.
func callMCPTool(t *testing.T, baseURL, token, name string, args map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result map[string]any `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || out.Result == nil {
		t.Fatalf("invalid MCP response: status=%d, %+v", resp.StatusCode, out)
	}
	return out.Result
}
