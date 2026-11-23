package memory_test

import (
	"net/http"
	"testing"
	"time"

	"engram/internal/apperror"
	"engram/internal/auth"
	"engram/internal/memory"

	"github.com/google/uuid"
)

// TestScopedMemberPermissions verifies independent grants, revocation, replay safety, and console isolation.
func TestScopedMemberPermissions(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()
	tenant, user, token, ctx := env.createTenantAndPrincipal(t, "Scoped", "scoped-user", "global_writer")
	marker, otherMarker := uuid.New(), uuid.New()
	project := env.registerProject(t, tenant, "Allowed", &marker, "", "")
	other := env.registerProject(t, tenant, "Other", &otherMarker, "", "")
	env.addProjectMember(t, project, user, "writer")
	env.addProjectMember(t, other, user, "writer")
	discover, err := env.svc.Discover(ctx, memory.DiscoverRequest{ProjectMarker: marker.String()})
	if err != nil {
		t.Fatal(err)
	}
	otherDisc, err := env.svc.Discover(ctx, memory.DiscoverRequest{ProjectMarker: otherMarker.String()})
	if err != nil {
		t.Fatal(err)
	}
	global, err := env.svc.Create(ctx, memory.CreateRequest{ProjectContext: discover.ProjectContext, Scope: "global", Category: "private", Title: "Global secret", Content: "hidden", IsPinned: true, IdempotencyKey: "global"}, "global")
	if err != nil {
		t.Fatal(err)
	}
	created, err := env.svc.Create(ctx, memory.CreateRequest{ProjectContext: discover.ProjectContext, Scope: "project", Category: "testing", Title: "Allowed", Content: "body", IdempotencyKey: "project"}, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Create(ctx, memory.CreateRequest{ProjectContext: otherDisc.ProjectContext, Scope: "project", Category: "testing", Title: "Other secret", Content: "hidden", IdempotencyKey: "other"}, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET role = 'none' WHERE tenant_id=$1 AND principal_id=$2`, tenant, user); err != nil {
		t.Fatal(err)
	}
	if _, err := env.database.Pool.Exec(env.ctx, `DELETE FROM project_members WHERE project_id=$1 AND principal_id=$2`, other, user); err != nil {
		t.Fatal(err)
	}

	t.Run("project only discovery and default query", func(t *testing.T) {
		disc, err := env.svc.Discover(ctx, memory.DiscoverRequest{ProjectMarker: marker.String()})
		if err != nil {
			t.Fatal(err)
		}
		if disc.Permissions.CanReadGlobal || disc.Permissions.CanWriteGlobal || !disc.Permissions.CanWriteProject || disc.Permissions.CanMaintainProject {
			t.Fatalf("unexpected permissions: %+v", disc.Permissions)
		}
		if len(disc.GlobalIndex.Pinned)+len(disc.GlobalIndex.Recent)+len(disc.GlobalIndex.CategoryStats) != 0 || disc.GlobalIndex.TotalCount != 0 || disc.GlobalIndex.Truncated {
			t.Fatalf("global index leaked: %+v", disc.GlobalIndex)
		}
		result, err := env.svc.Query(ctx, memory.QueryRequest{ProjectContext: disc.ProjectContext})
		if err != nil || len(result.Items) != 1 || result.Items[0].ID != created.Memory.ID {
			t.Fatalf("project query: %+v, %v", result, err)
		}
		_, err = env.svc.Query(ctx, memory.QueryRequest{ProjectContext: disc.ProjectContext, Scopes: []string{"global"}})
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		_, err = env.svc.Read(ctx, memory.ReadRequest{ProjectContext: disc.ProjectContext, MemoryID: global.Memory.ID.String()})
		assertAppErrorCode(t, err, apperror.CodeMemoryNotFound)
		changes, err := env.svc.Changes(ctx, memory.ChangesRequest{ProjectContext: disc.ProjectContext})
		if err != nil {
			t.Fatal(err)
		}
		if len(changes.Events) != 1 || changes.Events[0].MemoryID != created.Memory.ID {
			t.Fatalf("changes leaked another scope: %+v", changes.Events)
		}
		otherAccess, err := env.svc.Discover(ctx, memory.DiscoverRequest{ProjectMarker: otherMarker.String()})
		if err != nil {
			t.Fatal(err)
		}
		// Even though this tenant already has events, an unreadable context must reveal no high-water mark.
		signer := auth.NewTokenSigner(env.cfg.Server.ContextSignKey, time.Hour)
		revision, err := signer.VerifyCursor(otherAccess.LatestCursor, tenant, nil)
		if err != nil || revision != 0 {
			t.Fatalf("unreadable context cursor revision=%d err=%v", revision, err)
		}
		_, err = env.svc.Query(ctx, memory.QueryRequest{ProjectContext: otherAccess.ProjectContext})
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		_, err = env.svc.Changes(ctx, memory.ChangesRequest{ProjectContext: otherAccess.ProjectContext})
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		request, err := http.NewRequest(http.MethodGet, env.mcpServer.URL+"/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "text/event-stream")
		response, err := env.mcpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("unscoped stream status = %d", response.StatusCode)
		}
	})

	newTitle := "Updated"
	update := memory.UpdateRequest{ProjectContext: discover.ProjectContext, MemoryID: created.Memory.ID.String(), ExpectedVersion: 1, Patch: memory.MemoryPatch{Title: &newTitle}, IdempotencyKey: "update"}
	t.Run("read only cannot write or delete", func(t *testing.T) {
		env.addProjectMember(t, project, user, "reader")
		_, err := env.svc.Update(ctx, update, "update")
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		_, err = env.svc.Create(ctx, memory.CreateRequest{ProjectContext: discover.ProjectContext, Scope: "project", Category: "testing", Title: "Denied", Content: "body", IdempotencyKey: "denied"}, "denied")
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		_, err = env.svc.Delete(ctx, memory.DeleteRequest{ProjectContext: discover.ProjectContext, MemoryID: created.Memory.ID.String(), ExpectedVersion: 1, IdempotencyKey: "delete-denied"}, "denied")
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
	})
	t.Run("writer can update and soft delete but cannot manage", func(t *testing.T) {
		env.addProjectMember(t, project, user, "writer")
		if _, err := env.svc.Update(ctx, update, "update"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.Delete(ctx, memory.DeleteRequest{ProjectContext: discover.ProjectContext, MemoryID: created.Memory.ID.String(), ExpectedVersion: 2, IdempotencyKey: "delete"}, "delete"); err != nil {
			t.Fatal(err)
		}
		_, err := env.svc.Restore(env.ctx, memory.RestoreRequest{TenantID: tenant, ActorID: user, MemoryID: created.Memory.ID, ExpectedVersion: 3})
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		_, err = env.svc.AdminPurge(env.ctx, memory.AdminPurgeRequest{TenantID: tenant, ActorID: user})
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		actor := uuid.New()
		if _, err := env.database.Pool.Exec(env.ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,'system_webui','admin','Console')`, actor); err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.Restore(env.ctx, memory.RestoreRequest{TenantID: tenant, ActorID: actor, MemoryID: created.Memory.ID, ExpectedVersion: 3}); err != nil {
			t.Fatal(err)
		}
		// Even an accidentally assigned membership cannot turn the audit identity into an API member.
		if _, err := env.database.Pool.Exec(env.ctx, `INSERT INTO tenant_members(tenant_id,principal_id,role) VALUES($1,$2,'global_writer')`, tenant, actor); err != nil {
			t.Fatal(err)
		}
		authenticator, err := auth.NewAuthenticator(env.database.Pool, env.cfg.Auth)
		if err != nil {
			t.Fatal(err)
		}
		_, err = authenticator.VerifyPrincipalMembership(env.ctx, tenant, actor)
		assertAppErrorCode(t, err, apperror.CodeUnauthenticated)
	})
	t.Run("revoked grants reject old context and cached writes", func(t *testing.T) {
		if _, err := env.database.Pool.Exec(env.ctx, `DELETE FROM project_members WHERE project_id=$1 AND principal_id=$2`, project, user); err != nil {
			t.Fatal(err)
		}
		_, err := env.svc.Update(ctx, update, "replay")
		assertAppErrorCode(t, err, apperror.CodeMemoryNotFound)
		_, err = env.svc.Create(ctx, memory.CreateRequest{ProjectContext: discover.ProjectContext, Scope: "global", Category: "private", Title: "Global secret", Content: "hidden", IsPinned: true, IdempotencyKey: "global"}, "replay")
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		_, err = env.svc.Query(ctx, memory.QueryRequest{ProjectContext: discover.ProjectContext, Scopes: []string{"project"}})
		assertAppErrorCode(t, err, apperror.CodeUnauthorized)
		_, err = env.svc.Read(ctx, memory.ReadRequest{ProjectContext: discover.ProjectContext, MemoryID: created.Memory.ID.String()})
		assertAppErrorCode(t, err, apperror.CodeMemoryNotFound)
	})
}
