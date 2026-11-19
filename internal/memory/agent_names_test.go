package memory_test

import (
	"encoding/json"
	"strings"
	"testing"

	"engram/internal/apperror"
	"engram/internal/memory"

	"github.com/google/uuid"
)

// TestMemoryAgentAttribution follows distinct agents through versions without changing member identity.
func TestMemoryAgentAttribution(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()
	tenant, member, token, ctx := env.createTenantAndPrincipal(t, "Agent audit", "owner", "global_writer")
	marker := uuid.New()
	projectID := env.registerProject(t, tenant, "Agent project", &marker, "", "")
	env.addProjectMember(t, projectID, member, "writer")
	discovery, err := env.svc.Discover(ctx, memory.DiscoverRequest{ProjectMarker: marker.String()})
	if err != nil {
		t.Fatal(err)
	}
	request := memory.CreateRequest{ProjectContext: discovery.ProjectContext, Scope: "project", Category: "testing", Title: "Audit", Content: "first", AgentName: "  Codex  ", IdempotencyKey: "agent-create"}
	created, err := env.svc.Create(ctx, request, "agent-create")
	if err != nil {
		t.Fatal(err)
	}
	id := created.Memory.ID.String()
	if created.Memory.CreatedAgent != "Codex" || created.Memory.UpdatedAgent != "Codex" || created.Memory.CreatedBy != member || created.Memory.UpdatedBy != member {
		t.Fatalf("wrong attribution: %+v", created.Memory)
	}
	request.AgentName = "Codex"
	replayed, err := env.svc.Create(ctx, request, "agent-create-retry")
	if err != nil || replayed.Memory.ID != created.Memory.ID {
		t.Fatalf("normalized retry: %+v, %v", replayed, err)
	}
	request.AgentName = "Claude Code"
	_, err = env.svc.Create(ctx, request, "different-agent")
	assertAppErrorCode(t, err, apperror.CodeIdempotencyConflict)

	content := "second"
	update := memory.UpdateRequest{ProjectContext: discovery.ProjectContext, MemoryID: id, ExpectedVersion: 1, Patch: memory.MemoryPatch{Content: &content}, AgentName: "Claude Code", IdempotencyKey: "agent-update"}
	updated, err := env.svc.Update(ctx, update, "agent-update")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Memory.CreatedAgent != "Codex" || updated.Memory.UpdatedAgent != "Claude Code" || updated.Memory.UpdatedBy != member {
		t.Fatalf("update replaced creator: %+v", updated.Memory)
	}
	update.AgentName = "review-agent"
	_, err = env.svc.Update(ctx, update, "different-agent")
	assertAppErrorCode(t, err, apperror.CodeIdempotencyConflict)
	for version, agent := range map[int64]string{1: "Codex", 2: "Claude Code"} {
		read, err := env.svc.Read(ctx, memory.ReadRequest{ProjectContext: discovery.ProjectContext, MemoryID: id, Version: &version})
		if err != nil || read.Memory.CreatedAgent != "Codex" || read.Memory.UpdatedAgent != agent {
			t.Fatalf("version %d: %+v, %v", version, read, err)
		}
	}

	// A legacy client must clear the last agent instead of attributing its write to the previous agent.
	content = "legacy update"
	legacy, err := env.svc.Update(ctx, memory.UpdateRequest{ProjectContext: discovery.ProjectContext, MemoryID: id, ExpectedVersion: 2, Patch: memory.MemoryPatch{Content: &content}, IdempotencyKey: "legacy-update"}, "legacy-update")
	if err != nil || legacy.Memory.CreatedAgent != "Codex" || legacy.Memory.UpdatedAgent != "" {
		t.Fatalf("legacy attribution: %+v, %v", legacy, err)
	}
	read, err := env.svc.Read(ctx, memory.ReadRequest{ProjectContext: discovery.ProjectContext, MemoryID: id})
	if err != nil || read.Memory.UpdatedAgent != "" || read.Memory.CreatedAgent != "Codex" {
		t.Fatalf("persisted attribution: %+v, %v", read, err)
	}

	promote := memory.PromoteRequest{ProjectContext: discovery.ProjectContext, MemoryID: id, ExpectedVersion: 3, AgentName: "review-agent", IdempotencyKey: "agent-promote"}
	promoted, err := env.svc.Promote(ctx, promote, "agent-promote")
	if err != nil || promoted.Memory.CreatedAgent != "review-agent" || promoted.Memory.UpdatedAgent != "review-agent" {
		t.Fatalf("promotion attribution: %+v, %v", promoted, err)
	}
	promote.AgentName = "different"
	_, err = env.svc.Promote(ctx, promote, "different-agent")
	assertAppErrorCode(t, err, apperror.CodeIdempotencyConflict)
	deleted, err := env.svc.Delete(ctx, memory.DeleteRequest{ProjectContext: discovery.ProjectContext, MemoryID: id, ExpectedVersion: 3, AgentName: "cleanup-agent", IdempotencyKey: "agent-delete"}, "agent-delete")
	if err != nil {
		t.Fatal(err)
	}
	version := deleted.Version
	read, err = env.svc.Read(ctx, memory.ReadRequest{ProjectContext: discovery.ProjectContext, MemoryID: id, Version: &version})
	if err != nil || read.Memory.UpdatedAgent != "cleanup-agent" || read.Memory.CreatedAgent != "Codex" {
		t.Fatalf("delete version attribution: %+v, %v", read, err)
	}

	// Console changes have no agent and must not carry the deleted version's agent into a new version.
	actor := uuid.New()
	if _, err := env.database.Pool.Exec(env.ctx, `INSERT INTO principals(id,identity_provider,subject,display_name) VALUES($1,'system_webui','admin','Console')`, actor); err != nil {
		t.Fatal(err)
	}
	restored, err := env.svc.Restore(env.ctx, memory.RestoreRequest{TenantID: tenant, ActorID: actor, MemoryID: created.Memory.ID, ExpectedVersion: deleted.Version})
	if err != nil || restored.UpdatedAgent != "" || restored.CreatedAgent != "Codex" {
		t.Fatalf("console restore attribution: %+v, %v", restored, err)
	}
	pinned, err := env.svc.AdminSetPinned(env.ctx, memory.AdminPinRequest{TenantID: tenant, ActorID: actor, MemoryID: promoted.Memory.ID, ExpectedVersion: 1, IsPinned: true})
	if err != nil || pinned.UpdatedAgent != "" || pinned.CreatedAgent != "review-agent" {
		t.Fatalf("console update attribution: %+v, %v", pinned, err)
	}
	count, err := env.svc.AdminPurge(env.ctx, memory.AdminPurgeRequest{TenantID: tenant, ActorID: actor, Scope: "project", ProjectID: &projectID, MemoryID: &created.Memory.ID, Mode: memory.PurgeSoft, Digest: memory.PurgeDigest([]memory.MemoryVersion{{ID: created.Memory.ID, Version: restored.Version}})})
	if err != nil || count != 1 {
		t.Fatalf("console purge: %d, %v", count, err)
	}
	version = restored.Version + 1
	read, err = env.svc.Read(ctx, memory.ReadRequest{ProjectContext: discovery.ProjectContext, MemoryID: id, Version: &version})
	if err != nil || read.Memory.UpdatedAgent != "" {
		t.Fatalf("console purge attribution: %+v, %v", read, err)
	}

	// Exercise strict MCP argument decoding and the optional agent parameter over HTTP.
	result := callMCPTool(t, env.mcpServer.URL, token, "memory_create", map[string]any{"project_context": discovery.ProjectContext, "scope": "global", "category": "testing", "title": "MCP", "content": "body", "agent_name": "Codex", "idempotency_key": "mcp-agent"})
	if result["isError"] == true {
		t.Fatalf("MCP write rejected agent_name: %+v", result)
	}
	items := result["content"].([]any)
	var response memory.WriteMemoryResponse
	if err := json.Unmarshal([]byte(items[0].(map[string]any)["text"].(string)), &response); err != nil || response.Memory.CreatedAgent != "Codex" {
		t.Fatalf("MCP agent attribution: %+v, %v", response, err)
	}

	if _, err := env.database.Pool.Exec(env.ctx, `UPDATE tenant_members SET role='none' WHERE tenant_id=$1 AND principal_id=$2`, tenant, member); err != nil {
		t.Fatal(err)
	}
	request.Scope = "global"
	request.AgentName = "admin"
	request.IdempotencyKey = "agent-does-not-authorize"
	_, err = env.svc.Create(ctx, request, "unauthorized-agent")
	assertAppErrorCode(t, err, apperror.CodeUnauthorized)
}

// TestAgentNameValidation rejects malformed metadata on every write and supports Unicode display names.
func TestAgentNameValidation(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()
	_, _, _, ctx := env.createTenantAndPrincipal(t, "Validation", "owner", "global_writer")
	disc, err := env.svc.Discover(ctx, memory.DiscoverRequest{AllowGlobalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Repeat("a", 129), "Codex\nagent", "Claude\x00Code"} {
		_, err = env.svc.Create(ctx, memory.CreateRequest{ProjectContext: disc.ProjectContext, AgentName: invalid}, "invalid")
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)
		_, err = env.svc.Update(ctx, memory.UpdateRequest{ProjectContext: disc.ProjectContext, AgentName: invalid}, "invalid")
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)
		_, err = env.svc.Delete(ctx, memory.DeleteRequest{ProjectContext: disc.ProjectContext, AgentName: invalid}, "invalid")
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)
		_, err = env.svc.Promote(ctx, memory.PromoteRequest{ProjectContext: disc.ProjectContext, AgentName: invalid}, "invalid")
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)
	}
	for _, name := range []string{"", "  ", strings.Repeat("中", 128)} {
		request := memory.CreateRequest{ProjectContext: disc.ProjectContext, Scope: "global", Category: "testing", Title: "Allowed", Content: "body", AgentName: name, IdempotencyKey: uuid.NewString()}
		response, err := env.svc.Create(ctx, request, "valid")
		if err != nil || response.Memory.CreatedAgent != strings.TrimSpace(name) {
			t.Fatalf("valid agent: %+v, %v", response, err)
		}
		if strings.TrimSpace(name) == "" {
			// This fixed hash was produced by the pre-agent request format and protects upgrade retries.
			const legacyHash = "2596d98bb62c458bf7bdb9679f1f34c2c754ce3d0c56ea34f8100028ec8a4ce5"
			var hash string
			if err := env.database.Pool.QueryRow(env.ctx, `SELECT request_hash FROM idempotency_requests WHERE tool_name='memory_create' AND idempotency_key=$1`, request.IdempotencyKey).Scan(&hash); err != nil || hash != legacyHash {
				t.Fatalf("legacy request hash changed: hash=%s err=%v", hash, err)
			}
			request.AgentName = ""
			replay, err := env.svc.Create(ctx, request, "legacy-retry")
			if err != nil || replay.Memory.ID != response.Memory.ID {
				t.Fatalf("unnamed retry changed identity: %+v, %v", replay, err)
			}
		}
	}
}
