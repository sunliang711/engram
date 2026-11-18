package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"engram/internal/admin"
	"engram/internal/apperror"
	"engram/internal/auth"
	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/exporter"
	"engram/internal/mcp"
	"engram/internal/memory"
	"engram/internal/notify"
	"engram/internal/observability"
	"engram/internal/project"
	"engram/internal/security"

	"github.com/google/uuid"
)

type testEnv struct {
	ctx       context.Context
	cfg       config.Config
	database  *db.Database
	svc       *memory.Service
	adminAuth *admin.AuthManager
	mcpServer *httptest.Server
	cleanup   func()
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("ENGRAM_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/engram_test?sslmode=disable"
	}

	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	cfg.Database.AutoMigrate = true
	cfg.Export.OutputDir = t.TempDir()

	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Skipf("skipping postgres integration test (database unavailable at %s): %v", dsn, err)
	}

	// Clean all tables before test
	_, err = database.Pool.Exec(ctx, `
		TRUNCATE TABLE
			admin_accounts,
			idempotency_requests,
			memory_events,
			memory_revisions,
			memories,
			project_repositories,
			project_members,
			projects,
			api_tokens,
			tenant_members,
			principals,
			tenants
		RESTART IDENTITY CASCADE
	`)
	if err != nil {
		database.Close()
		t.Fatalf("truncate tables: %v", err)
	}

	log := observability.NewLogger("error", os.Stderr)
	authenticator, err := auth.NewAuthenticator(database.Pool, cfg.Auth)
	if err != nil {
		database.Close()
		t.Fatalf("new authenticator: %v", err)
	}
	signer := auth.NewTokenSigner(cfg.Server.ContextSignKey, cfg.Server.ContextTTL)
	rateLimiter := auth.NewRateLimiter(1000, 200)
	resolver := project.NewResolver(database.Pool)
	metrics := observability.NewMetrics()
	hub := notify.NewHub(database.Pool, signer, log)
	exp := exporter.NewExporter(database.Pool, cfg.Export, metrics, log)
	adminAuth := admin.NewAuthManager(database.Pool, cfg.Server.ContextSignKey, cfg.Server.ContextTTL, false)

	listenCtx, cancelListen := context.WithCancel(ctx)
	go hub.RunListenLoop(listenCtx)

	svc := memory.NewService(database.Pool, cfg, authenticator, signer, rateLimiter, resolver, hub, metrics, log)
	mcpSrv := mcp.NewServer(database, authenticator, signer, svc, hub, metrics, log)
	adminHandler := admin.NewHandler(database.Pool, adminAuth, svc, exp, log)
	adminHandler.RegisterRoutes(mcpSrv.Mux())

	httpSrv := httptest.NewServer(mcpSrv.Handler())

	return &testEnv{
		ctx:       ctx,
		cfg:       cfg,
		database:  database,
		svc:       svc,
		adminAuth: adminAuth,
		mcpServer: httpSrv,
		cleanup: func() {
			cancelListen()
			httpSrv.Close()
			database.Close()
		},
	}
}

func (e *testEnv) createTenantAndPrincipal(t *testing.T, tenantName, subject, tenantRole string) (uuid.UUID, uuid.UUID, string, context.Context) {
	t.Helper()
	tenantID := uuid.New()
	principalID := uuid.New()
	rawToken := "aim_test_" + uuid.NewString()

	_, err := e.database.Pool.Exec(e.ctx, `INSERT INTO tenants (id, name, status) VALUES ($1, $2, 'active')`, tenantID, tenantName)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	_, err = e.database.Pool.Exec(e.ctx, `
		INSERT INTO principals (id, identity_provider, subject, display_name, status)
		VALUES ($1, 'api_token', $2, $3, 'active')
	`, principalID, subject, subject)
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	_, err = e.database.Pool.Exec(e.ctx, `
		INSERT INTO tenant_members (tenant_id, principal_id, role)
		VALUES ($1, $2, $3)
	`, tenantID, principalID, tenantRole)
	if err != nil {
		t.Fatalf("insert tenant_member: %v", err)
	}
	_, err = e.database.Pool.Exec(e.ctx, `
		INSERT INTO api_tokens (id, token_hash, token_prefix, tenant_id, principal_id, name)
		VALUES ($1, $2, $3, $4, $5, 'test')
	`, uuid.New(), auth.HashToken(rawToken), rawToken[:12], tenantID, principalID)
	if err != nil {
		t.Fatalf("insert api_token: %v", err)
	}

	pCtx := auth.WithPrincipal(e.ctx, auth.Principal{
		TenantID:    tenantID,
		PrincipalID: principalID,
		TenantRole:  tenantRole,
		DisplayName: subject,
	})
	return tenantID, principalID, rawToken, pCtx
}

func (e *testEnv) registerProject(t *testing.T, tenantID uuid.UUID, name string, marker *uuid.UUID, remoteURL, wsPath string) uuid.UUID {
	t.Helper()
	projID := uuid.New()
	_, err := e.database.Pool.Exec(e.ctx, `
		INSERT INTO projects (id, tenant_id, name, marker, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, projID, tenantID, name, marker)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if remoteURL != "" {
		canon, err := project.NormalizeRemote(remoteURL)
		if err != nil {
			t.Fatalf("normalize remote: %v", err)
		}
		_, err = e.database.Pool.Exec(e.ctx, `
			INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary)
			VALUES ($1, $2, $3, $4, $5, true)
		`, uuid.New(), tenantID, projID, canon, wsPath)
		if err != nil {
			t.Fatalf("insert project_repository: %v", err)
		}
	}
	return projID
}

func (e *testEnv) addProjectMember(t *testing.T, projectID, principalID uuid.UUID, role string) {
	t.Helper()
	_, err := e.database.Pool.Exec(e.ctx, `
		INSERT INTO project_members (project_id, principal_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id, principal_id) DO UPDATE SET role = EXCLUDED.role
	`, projectID, principalID, role)
	if err != nil {
		t.Fatalf("insert project_member: %v", err)
	}
}

func TestV1AcceptanceSuite(t *testing.T) {
	env := setupTestEnv(t)
	defer env.cleanup()

	// Setup Tenant A and Tenant B
	tenantA, userA, tokenA, ctxA := env.createTenantAndPrincipal(t, "Tenant-A", "alice", "global_writer")
	tenantB, userB, _, ctxB := env.createTenantAndPrincipal(t, "Tenant-B", "bob", "admin")

	// Register Project A1 (with SSH remote and alias) and Project A2 in Tenant A
	rootMarkerA1 := uuid.New()
	projA1 := env.registerProject(t, tenantA, "project-a1", &rootMarkerA1, "git@github.com:eagle/project-a.git", "")
	env.addProjectMember(t, projA1, userA, "maintainer")

	// Add an alias remote to Project A1
	_, err := env.database.Pool.Exec(env.ctx, `
		INSERT INTO project_repositories (id, tenant_id, project_id, canonical_remote, workspace_path, is_primary)
		VALUES ($1, $2, $3, 'gitlab.example.com/eagle/project-a-mirror', '', false)
	`, uuid.New(), tenantA, projA1)
	if err != nil {
		t.Fatalf("insert alias remote: %v", err)
	}

	// Register Monorepo subprojects under github.com/eagle/project-a:
	// - services/api (by remote + workspace_path)
	// - services/payment (by subproject marker)
	projMonoAPI := env.registerProject(t, tenantA, "mono-api", nil, "https://github.com/eagle/project-a.git", "services/api")
	env.addProjectMember(t, projMonoAPI, userA, "maintainer")

	subMarkerPayment := uuid.New()
	projMonoPayment := env.registerProject(t, tenantA, "mono-payment", &subMarkerPayment, "", "")
	env.addProjectMember(t, projMonoPayment, userA, "maintainer")

	// Register Project A2 in Tenant A
	projA2 := env.registerProject(t, tenantA, "project-a2", nil, "git@github.com:eagle/project-a2.git", "")
	env.addProjectMember(t, projA2, userA, "maintainer")

	// Register Project B1 in Tenant B
	markerB1 := uuid.New()
	projB1 := env.registerProject(t, tenantB, "project-b1", &markerB1, "git@github.com:tenantb/secret-repo.git", "")
	_ = userB

	// =========================================================================
	// Acceptance 6 & 7: Remote Normalization, Aliases, and Monorepo Precedence
	// =========================================================================
	t.Run("Acceptance_6_and_7_RemoteAliases_And_MonorepoPrecedence", func(t *testing.T) {
		// SSH, HTTPS, and alias resolve to projA1 when at root
		for _, remoteURL := range []string{
			"git@github.com:eagle/project-a.git",
			"https://github.com/eagle/project-a.git",
			"ssh://git@github.com/eagle/project-a.git",
			"https://gitlab.example.com/eagle/project-a-mirror.git",
		} {
			disc, err := env.svc.Discover(ctxA, memory.DiscoverRequest{
				Remotes: []project.RemoteInput{{Name: "origin", URL: remoteURL}},
			})
			if err != nil {
				t.Fatalf("Discover(%s) failed: %v", remoteURL, err)
			}
			if disc.Project == nil || disc.Project.ID != projA1 {
				t.Fatalf("Discover(%s) resolved project %v, want %v", remoteURL, disc.Project, projA1)
			}
		}

		// Monorepo Rule 4: Git root Marker MUST NOT override more specific registered workspace_path ("services/api/v1")
		discAPI, err := env.svc.Discover(ctxA, memory.DiscoverRequest{
			ProjectMarker: rootMarkerA1.String(),
			MarkerPath:    "", // found at Git root
			Remotes:       []project.RemoteInput{{Name: "origin", URL: "git@github.com:eagle/project-a.git"}},
			WorkspacePath: "services/api/handlers",
		})
		if err != nil {
			t.Fatalf("Discover monorepo workspace_path failed: %v", err)
		}
		if discAPI.Project == nil || discAPI.Project.ID != projMonoAPI {
			t.Fatalf("expected monorepo subproject %v, got %v", projMonoAPI, discAPI.Project)
		}

		// Monorepo Rule 2: Subproject Marker ("services/payment") takes priority over Git root and repo
		discPay, err := env.svc.Discover(ctxA, memory.DiscoverRequest{
			ProjectMarker: subMarkerPayment.String(),
			MarkerPath:    "services/payment",
			Remotes:       []project.RemoteInput{{Name: "origin", URL: "git@github.com:eagle/project-a.git"}},
			WorkspacePath: "services/payment/internal",
		})
		if err != nil {
			t.Fatalf("Discover monorepo subproject marker failed: %v", err)
		}
		if discPay.Project == nil || discPay.Project.ID != projMonoPayment {
			t.Fatalf("expected monorepo payment project %v, got %v", projMonoPayment, discPay.Project)
		}

		// Absolute local path alone or with remotes must return INVALID_ARGUMENT (not silent global_only)
		_, err = env.svc.Discover(ctxA, memory.DiscoverRequest{
			WorkspacePath: "/Users/eagle/project-a",
		})
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)

		_, err = env.svc.Discover(ctxA, memory.DiscoverRequest{
			WorkspacePath: "/Users/eagle/project-a",
			Remotes:       []project.RemoteInput{{Name: "origin", URL: "git@github.com:eagle/project-a.git"}},
		})
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)

		// Relative workspace_path without remotes or project_marker must require allow_global_only
		_, err = env.svc.Discover(ctxA, memory.DiscoverRequest{
			WorkspacePath: "services/api",
		})
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)

		// Empty DiscoverRequest (non-Git workspace) falls back to global_only
		discEmpty, err := env.svc.Discover(ctxA, memory.DiscoverRequest{})
		if err != nil {
			t.Fatalf("Discover empty request failed: %v", err)
		}
		if discEmpty.Mode != auth.ModeGlobalOnly {
			t.Fatalf("expected mode global_only for empty discover, got %q", discEmpty.Mode)
		}
	})

	// Discover contexts for Project A1, Project A2, and Project B1
	discA1, err := env.svc.Discover(ctxA, memory.DiscoverRequest{
		Remotes: []project.RemoteInput{{Name: "origin", URL: "git@github.com:eagle/project-a.git"}},
	})
	if err != nil {
		t.Fatalf("discover A1: %v", err)
	}
	discA2, err := env.svc.Discover(ctxA, memory.DiscoverRequest{
		Remotes: []project.RemoteInput{{Name: "origin", URL: "git@github.com:eagle/project-a2.git"}},
	})
	if err != nil {
		t.Fatalf("discover A2: %v", err)
	}
	discB1, err := env.svc.Discover(ctxB, memory.DiscoverRequest{
		Remotes: []project.RemoteInput{{Name: "origin", URL: "git@github.com:tenantb/secret-repo.git"}},
	})
	if err != nil {
		t.Fatalf("discover B1: %v", err)
	}

	// =========================================================================
	// Acceptance 1 & 5: Optimistic Locking Conflict & Idempotency
	// =========================================================================
	var memA1ID uuid.UUID
	t.Run("Acceptance_1_and_5_OptimisticLock_And_Idempotency", func(t *testing.T) {
		createReq := memory.CreateRequest{
			ProjectContext: discA1.ProjectContext,
			Scope:          "project",
			Category:       "architecture",
			Title:          "鉴权与分布式事务规范",
			Content:        "在微服务架构中，网关负责统一鉴权，核心账务使用幂等键与分布式锁保证一致性。",
			Tags:           []string{"auth", "transaction"},
			IsPinned:       true,
			SourceRefs: []security.SourceRef{
				{Type: "file", Ref: "DESIGN.md", Title: "Architecture Spec"},
			},
			IdempotencyKey: "idem-create-a1-1",
		}
		created1, err := env.svc.Create(ctxA, createReq, "req-1")
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		memA1ID = created1.Memory.ID

		// Repeat exact same request with same idempotency_key -> returns identical memory & version 1
		created2, err := env.svc.Create(ctxA, createReq, "req-2")
		if err != nil {
			t.Fatalf("Idempotent Create replay failed: %v", err)
		}
		if created2.Memory.ID != created1.Memory.ID || created2.Memory.Version != 1 {
			t.Fatalf("Idempotent replay mismatch: got %v v%d, want %v v1", created2.Memory.ID, created2.Memory.Version, created1.Memory.ID)
		}

		// Same idempotency_key with different content -> IDEMPOTENCY_CONFLICT
		conflictReq := createReq
		conflictReq.Content = "Different content body"
		_, err = env.svc.Create(ctxA, conflictReq, "req-3")
		assertAppErrorCode(t, err, apperror.CodeIdempotencyConflict)

		// Two concurrent Agents updating based on expected_version = 1: only 1 succeeds, the other gets VERSION_CONFLICT
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				newBody := "并发更新内容-" + string(rune('A'+idx)) + "：包含鉴权、幂等与分布式锁说明。"
				_, errs[idx] = env.svc.Update(ctxA, memory.UpdateRequest{
					ProjectContext:  discA1.ProjectContext,
					MemoryID:        memA1ID.String(),
					ExpectedVersion: 1,
					Patch: memory.MemoryPatch{
						Content: &newBody,
					},
					IdempotencyKey: "concurrent-update-" + uuid.NewString(),
				}, "req-conc")
			}(i)
		}
		wg.Wait()

		successCount := 0
		conflictCount := 0
		for _, e := range errs {
			if e == nil {
				successCount++
			} else {
				var appErr *apperror.Error
				if errors.As(e, &appErr) && appErr.Code == apperror.CodeVersionConflict {
					conflictCount++
				} else {
					t.Fatalf("unexpected error in concurrent update: %v", e)
				}
			}
		}
		if successCount != 1 || conflictCount != 1 {
			t.Fatalf("expected 1 success and 1 VERSION_CONFLICT, got success=%d conflict=%d", successCount, conflictCount)
		}
	})

	// =========================================================================
	// Acceptance 3 & 4: Project Scope Isolation & Cross-Tenant Anti-Probing
	// =========================================================================
	t.Run("Acceptance_3_and_4_ProjectAndTenantIsolation", func(t *testing.T) {
		// Create a memory in Project A2 and Project B1
		memA2, err := env.svc.Create(ctxA, memory.CreateRequest{
			ProjectContext: discA2.ProjectContext,
			Scope:          "project",
			Category:       "ops",
			Title:          "Project A2 Secret Deployment",
			Content:        "Only visible inside Project A2.",
			IdempotencyKey: "create-a2-1",
		}, "req-a2")
		if err != nil {
			t.Fatalf("create A2 memory: %v", err)
		}

		memB1, err := env.svc.Create(ctxB, memory.CreateRequest{
			ProjectContext: discB1.ProjectContext,
			Scope:          "project",
			Category:       "security",
			Title:          "Tenant B Confidential Architecture",
			Content:        "Strictly confidential to Tenant B.",
			IdempotencyKey: "create-b1-1",
		}, "req-b1")
		if err != nil {
			t.Fatalf("create B1 memory: %v", err)
		}

		// Project A1 query cannot see Project A2's memory or Tenant B's memory
		qRes, err := env.svc.Query(ctxA, memory.QueryRequest{
			ProjectContext: discA1.ProjectContext,
			Scopes:         []string{"global", "project"},
		})
		if err != nil {
			t.Fatalf("query A1: %v", err)
		}
		for _, item := range qRes.Items {
			if item.ID == memA2.Memory.ID || item.ID == memB1.Memory.ID {
				t.Fatalf("Project A1 query leaked unauthorized memory %v", item.ID)
			}
		}

		// Project A1 attempting to Read/Update Project A2's memory or Tenant B's memory by ID -> MEMORY_NOT_FOUND (no existence leak!)
		_, err = env.svc.Read(ctxA, memory.ReadRequest{
			ProjectContext: discA1.ProjectContext,
			MemoryID:       memA2.Memory.ID.String(),
		})
		assertAppErrorCode(t, err, apperror.CodeMemoryNotFound)

		_, err = env.svc.Read(ctxA, memory.ReadRequest{
			ProjectContext: discA1.ProjectContext,
			MemoryID:       memB1.Memory.ID.String(),
		})
		assertAppErrorCode(t, err, apperror.CodeMemoryNotFound)

		newTitle := "Probe Update"
		_, err = env.svc.Update(ctxA, memory.UpdateRequest{
			ProjectContext:  discA1.ProjectContext,
			MemoryID:        memB1.Memory.ID.String(),
			ExpectedVersion: 1,
			Patch:           memory.MemoryPatch{Title: &newTitle},
			IdempotencyKey:  "probe-b1",
		}, "req-probe")
		assertAppErrorCode(t, err, apperror.CodeMemoryNotFound)

		// Tenant A probing Tenant B's project remote or marker -> PROJECT_NOT_REGISTERED
		_, err = env.svc.Discover(ctxA, memory.DiscoverRequest{
			ProjectMarker: markerB1.String(),
			Remotes:       []project.RemoteInput{{Name: "origin", URL: "git@github.com:tenantb/secret-repo.git"}},
		})
		assertAppErrorCode(t, err, apperror.CodeProjectNotRegistered)
		_ = projB1
	})

	// =========================================================================
	// Acceptance 8: 1-char, 2-char, and Longer Chinese & English FTS Search
	// =========================================================================
	t.Run("Acceptance_8_ChineseAndEnglishSearch", func(t *testing.T) {
		for _, kw := range []string{"锁", "鉴权", "幂等", "分布式"} {
			res, err := env.svc.Query(ctxA, memory.QueryRequest{
				ProjectContext: discA1.ProjectContext,
				Query:          kw,
			})
			if err != nil {
				t.Fatalf("search(%q) failed: %v", kw, err)
			}
			if len(res.Items) == 0 || res.Items[0].ID != memA1ID {
				t.Fatalf("search(%q) did not match expected memory %v; got %+v", kw, memA1ID, res.Items)
			}
		}
	})

	// =========================================================================
	// Acceptance 2, 12, 13: Event Ordering, Changes Feed, Promote, Delete & Restore
	// =========================================================================
	t.Run("Acceptance_2_12_13_Changes_Promote_Delete_Restore", func(t *testing.T) {
		// Record initial cursor from empty string
		initChanges, err := env.svc.Changes(ctxA, memory.ChangesRequest{
			ProjectContext: discA1.ProjectContext,
			Cursor:         "",
		})
		if err != nil {
			t.Fatalf("initial changes failed: %v", err)
		}
		cursor := initChanges.NextCursor

		// Promote memA1ID (version 2 after concurrent update) to Global Memory
		promoted, err := env.svc.Promote(ctxA, memory.PromoteRequest{
			ProjectContext:  discA1.ProjectContext,
			MemoryID:        memA1ID.String(),
			ExpectedVersion: 2,
			IdempotencyKey:  "promote-a1-1",
		}, "req-promote")
		if err != nil {
			t.Fatalf("Promote failed: %v", err)
		}
		if promoted.Memory.Scope != "global" || promoted.Memory.SourceMemoryID == nil || *promoted.Memory.SourceMemoryID != memA1ID {
			t.Fatalf("unexpected promoted memory: %+v", promoted.Memory)
		}
		// Original project memory must NOT be flagged as duplicate candidate of the new global memory
		if len(promoted.DuplicateCandidates) != 0 {
			t.Fatalf("expected 0 duplicate candidates on first promote, got %v", promoted.DuplicateCandidates)
		}

		// Soft-delete memA1ID (expected_version = 2)
		delRes, err := env.svc.Delete(ctxA, memory.DeleteRequest{
			ProjectContext:  discA1.ProjectContext,
			MemoryID:        memA1ID.String(),
			ExpectedVersion: 2,
			IdempotencyKey:  "delete-a1-1",
		}, "req-delete")
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}
		if delRes.Version != 3 {
			t.Fatalf("expected deleted version 3, got %d", delRes.Version)
		}

		// Reading latest version of deleted memory returns MEMORY_NOT_FOUND, but reading version=2 works!
		_, err = env.svc.Read(ctxA, memory.ReadRequest{
			ProjectContext: discA1.ProjectContext,
			MemoryID:       memA1ID.String(),
		})
		assertAppErrorCode(t, err, apperror.CodeMemoryNotFound)

		v2 := int64(2)
		histRead, err := env.svc.Read(ctxA, memory.ReadRequest{
			ProjectContext: discA1.ProjectContext,
			MemoryID:       memA1ID.String(),
			Version:        &v2,
		})
		if err != nil || histRead.Memory.Version != 2 {
			t.Fatalf("expected historical read version 2 to succeed, got err=%v", err)
		}

		// Management Restore (expected_version = 3 -> version = 4)
		restored, err := env.svc.Restore(env.ctx, memory.RestoreRequest{
			TenantID:        tenantA,
			ActorID:         userA,
			MemoryID:        memA1ID,
			ExpectedVersion: 3,
			RequestID:       "req-restore",
		})
		if err != nil {
			t.Fatalf("Restore failed: %v", err)
		}
		if restored.Version != 4 || restored.DeletedAt != nil {
			t.Fatalf("expected restored version 4 with nil DeletedAt, got %+v", restored)
		}

		// Pull incremental changes from cursor -> must contain promote, delete, and restore in strict monotonic order
		delta, err := env.svc.Changes(ctxA, memory.ChangesRequest{
			ProjectContext: discA1.ProjectContext,
			Cursor:         cursor,
		})
		if err != nil {
			t.Fatalf("Changes delta failed: %v", err)
		}
		if len(delta.Events) != 3 {
			t.Fatalf("expected 3 delta events (promote, delete, restore), got %d: %+v", len(delta.Events), delta.Events)
		}
		wantOps := []string{"promote", "delete", "restore"}
		for i, wantOp := range wantOps {
			if delta.Events[i].Operation != wantOp {
				t.Fatalf("event[%d] operation = %s, want %s", i, delta.Events[i].Operation, wantOp)
			}
			if i > 0 && delta.Events[i].Revision <= delta.Events[i-1].Revision {
				t.Fatalf("events not strictly increasing: %d <= %d", delta.Events[i].Revision, delta.Events[i-1].Revision)
			}
		}

		// Concurrent writers with incremental cursor polling must never miss any event (Acceptance 2)
		pollCursor := delta.NextCursor
		const workers = 8
		var wg sync.WaitGroup
		createdIDs := make(map[uuid.UUID]bool)
		var mu sync.Mutex

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				res, cErr := env.svc.Create(ctxA, memory.CreateRequest{
					ProjectContext: discA1.ProjectContext,
					Scope:          "project",
					Category:       "concurrency",
					Title:          "Concurrent Event Ordering Test",
					Content:        "Testing advisory lock event serialization.",
					IdempotencyKey: "conc-event-" + uuid.NewString(),
				}, "req-ce")
				if cErr != nil {
					t.Errorf("concurrent create error: %v", cErr)
					return
				}
				mu.Lock()
				createdIDs[res.Memory.ID] = true
				mu.Unlock()
			}(w)
		}

		// Poll changes concurrently while transactions are committing
		seenInFeed := make(map[uuid.UUID]bool)
		donePolling := make(chan struct{})
		go func() {
			defer close(donePolling)
			for i := 0; i < 20; i++ {
				chRes, cErr := env.svc.Changes(ctxA, memory.ChangesRequest{
					ProjectContext: discA1.ProjectContext,
					Cursor:         pollCursor,
					Limit:          2,
				})
				if cErr == nil {
					for _, ev := range chRes.Events {
						seenInFeed[ev.MemoryID] = true
					}
					pollCursor = chRes.NextCursor
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()

		wg.Wait()
		<-donePolling

		// Drain any remaining events after workers complete
		for {
			chRes, err := env.svc.Changes(ctxA, memory.ChangesRequest{
				ProjectContext: discA1.ProjectContext,
				Cursor:         pollCursor,
				Limit:          10,
			})
			if err != nil {
				t.Fatalf("final drain changes: %v", err)
			}
			for _, ev := range chRes.Events {
				seenInFeed[ev.MemoryID] = true
			}
			pollCursor = chRes.NextCursor
			if !chRes.HasMore {
				break
			}
		}

		for id := range createdIDs {
			if !seenInFeed[id] {
				t.Fatalf("missed event for concurrently committed memory %v!", id)
			}
		}
	})

	// =========================================================================
	// Acceptance 9 & 10: Tenant-Isolated Deterministic Markdown Export
	// =========================================================================
	t.Run("Acceptance_9_and_10_MarkdownExport", func(t *testing.T) {
		log := observability.NewLogger("error", os.Stderr)
		metrics := observability.NewMetrics()
		exp := exporter.NewExporter(env.database.Pool, env.cfg.Export, metrics, log)

		count1, err := exp.ExportAll(env.ctx)
		if err != nil || count1 == 0 {
			t.Fatalf("ExportAll failed: count=%d err=%v", count1, err)
		}

		expectedPath := filepath.Join(env.cfg.Export.OutputDir, "tenants", tenantA.String(), "projects", projA1.String(), memA1ID.String()+".md")
		bytes1, err := os.ReadFile(expectedPath)
		if err != nil {
			t.Fatalf("expected exported markdown file at %s: %v", expectedPath, err)
		}

		// Repeat export -> byte-for-byte identical
		_, err = exp.ExportAll(env.ctx)
		if err != nil {
			t.Fatalf("second ExportAll failed: %v", err)
		}
		bytes2, err := os.ReadFile(expectedPath)
		if err != nil || !bytes.Equal(bytes1, bytes2) {
			t.Fatalf("deterministic export check failed")
		}
	})

	// =========================================================================
	// End-to-End MCP Streamable HTTP Transport Check
	// =========================================================================
	t.Run("MCP_StreamableHTTP_EndToEnd", func(t *testing.T) {
		rpcBody, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "memory_discover",
				"arguments": map[string]any{
					"remotes": []map[string]string{
						{"name": "origin", "url": "git@github.com:eagle/project-a.git"},
					},
				},
			},
		})
		req, err := http.NewRequest(http.MethodPost, env.mcpServer.URL+"/mcp", bytes.NewReader(rpcBody))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do mcp http request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d", resp.StatusCode)
		}
		var out struct {
			Result map[string]json.RawMessage `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode mcp response: %v", err)
		}
		if _, hasStructured := out.Result["structuredContent"]; hasStructured {
			t.Fatalf("expected structuredContent to be omitted to avoid duplicate payload, got %s", string(out.Result["structuredContent"]))
		}
		var resultPayload struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		rawResult, _ := json.Marshal(out.Result)
		if err := json.Unmarshal(rawResult, &resultPayload); err != nil {
			t.Fatalf("unmarshal mcp result: %v", err)
		}
		if resultPayload.IsError || len(resultPayload.Content) != 1 || resultPayload.Content[0].Type != "text" {
			t.Fatalf("unexpected mcp discover result: %+v", resultPayload)
		}
		var discResp memory.DiscoverResponse
		if err := json.Unmarshal([]byte(resultPayload.Content[0].Text), &discResp); err != nil {
			t.Fatalf("unmarshal content text: %v", err)
		}
		if discResp.ProjectContext == "" {
			t.Fatalf("expected non-empty project_context in discover response: %+v", discResp)
		}
	})

	// =========================================================================
	// Web UI Admin Bootstrap, Forced Password Change, RESET_ADMIN, & Quickstart
	// =========================================================================
	t.Run("WebUI_Bootstrap_ResetAdmin_And_Quickstart", func(t *testing.T) {
		var buf bytes.Buffer
		tempPass1, err := env.adminAuth.Bootstrap(env.ctx, &buf)
		if err != nil || tempPass1 == "" {
			t.Fatalf("expected first-boot temporary password, got %q err=%v", tempPass1, err)
		}

		// Second startup without RESET_ADMIN must not regenerate password
		tempPassAgain, err := env.adminAuth.Bootstrap(env.ctx, &buf)
		if err != nil || tempPassAgain != "" {
			t.Fatalf("expected empty password on second boot without RESET_ADMIN, got %q err=%v", tempPassAgain, err)
		}

		// Login with temporary password -> must_change_password == true
		sessToken, state, err := env.adminAuth.Login(env.ctx, "admin", tempPass1)
		if err != nil || !state.Authenticated || !state.MustChangePassword {
			t.Fatalf("expected authenticated with must_change_password=true, got %+v err=%v", state, err)
		}

		// Accessing management endpoint before changing password must return 403 Forbidden
		req, _ := http.NewRequest(http.MethodGet, env.mcpServer.URL+"/api/admin/overview", nil)
		req.Header.Set("Authorization", "Bearer "+sessToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request overview: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden before password change, got %d", resp.StatusCode)
		}

		// Change password in Web UI
		changeBody, _ := json.Marshal(map[string]string{"new_password": "StrongAdminPassword2026!"})
		changeReq, _ := http.NewRequest(http.MethodPost, env.mcpServer.URL+"/api/admin/change-password", bytes.NewReader(changeBody))
		changeReq.Header.Set("Authorization", "Bearer "+sessToken)
		changeReq.Header.Set("Content-Type", "application/json")
		changeResp, err := http.DefaultClient.Do(changeReq)
		if err != nil {
			t.Fatalf("change password request: %v", err)
		}
		var newSessCookie string
		for _, c := range changeResp.Cookies() {
			if c.Name == admin.SessionCookieName {
				newSessCookie = c.Value
			}
		}
		changeResp.Body.Close()
		if changeResp.StatusCode != http.StatusOK || newSessCookie == "" {
			t.Fatalf("expected 200 OK and new session cookie, got status=%d", changeResp.StatusCode)
		}

		// Old temporary password must no longer work
		if _, _, err := env.adminAuth.Login(env.ctx, "admin", tempPass1); err == nil {
			t.Fatalf("expected old temporary password to be rejected after password change")
		}

		// Voluntary password change without current_password must be rejected (401 Unauthenticated)
		volBadBody, _ := json.Marshal(map[string]string{"new_password": "AnotherPassword2026!"})
		volBadReq, _ := http.NewRequest(http.MethodPost, env.mcpServer.URL+"/api/admin/change-password", bytes.NewReader(volBadBody))
		volBadReq.Header.Set("Authorization", "Bearer "+newSessCookie)
		volBadReq.Header.Set("Content-Type", "application/json")
		volBadResp, err := http.DefaultClient.Do(volBadReq)
		if err != nil {
			t.Fatalf("voluntary bad change password request: %v", err)
		}
		volBadResp.Body.Close()
		if volBadResp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized when changing password without current_password, got %d", volBadResp.StatusCode)
		}

		// Passing a change-feed cursor (cur_...) into memory_query must return INVALID_ARGUMENT
		badCur := discA1.LatestCursor
		_, err = env.svc.Query(ctxA, memory.QueryRequest{
			ProjectContext: discA1.ProjectContext,
			Cursor:         &badCur,
		})
		assertAppErrorCode(t, err, apperror.CodeInvalidArgument)

		// Keyset pagination in memory_query returns qcur_... and advances without overlap
		page1, err := env.svc.Query(ctxA, memory.QueryRequest{
			ProjectContext: discA1.ProjectContext,
			Limit:          2,
		})
		if err != nil || page1.NextCursor == nil {
			t.Fatalf("expected page1 with NextCursor, got err=%v", err)
		}
		page2, err := env.svc.Query(ctxA, memory.QueryRequest{
			ProjectContext: discA1.ProjectContext,
			Cursor:         page1.NextCursor,
			Limit:          2,
		})
		if err != nil || len(page2.Items) == 0 {
			t.Fatalf("expected page2 items, got err=%v", err)
		}
		if page1.Items[0].ID == page2.Items[0].ID {
			t.Fatalf("pagination returned duplicate item %v across page1 and page2", page1.Items[0].ID)
		}

		// Quickstart via Web UI with new session
		qsBody, _ := json.Marshal(map[string]string{
			"tenant_name":            "webui-tenant",
			"principal_subject":      "webui-agent",
			"principal_display_name": "WebUI Agent",
			"tenant_role":            "global_writer",
			"project_name":           "webui-project",
			"remote_url":             "git@github.com:eagle/webui-project.git",
		})
		qsReq, _ := http.NewRequest(http.MethodPost, env.mcpServer.URL+"/api/admin/quickstart", bytes.NewReader(qsBody))
		qsReq.Header.Set("Authorization", "Bearer "+newSessCookie)
		qsReq.Header.Set("Content-Type", "application/json")
		qsResp, err := http.DefaultClient.Do(qsReq)
		if err != nil {
			t.Fatalf("quickstart request: %v", err)
		}
		var qsOut struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(qsResp.Body).Decode(&qsOut)
		qsResp.Body.Close()
		if qsResp.StatusCode != http.StatusOK || qsOut.Token == "" {
			t.Fatalf("expected quickstart 200 OK with token, got status=%d", qsResp.StatusCode)
		}

		// Simulate RESET_ADMIN=true startup -> resets password to new temporary password and invalidates previous session
		resetMgr := admin.NewAuthManager(env.database.Pool, env.cfg.Server.ContextSignKey, env.cfg.Server.ContextTTL, true)
		tempPassReset, err := resetMgr.Bootstrap(env.ctx, &buf)
		if err != nil || tempPassReset == "" || tempPassReset == tempPass1 {
			t.Fatalf("expected new temporary password on RESET_ADMIN=true, got %q err=%v", tempPassReset, err)
		}

		// Previous session token must now be invalid because password_version incremented
		if _, err := resetMgr.VerifySession(env.ctx, newSessCookie); err == nil {
			t.Fatalf("expected previous session to be invalidated after RESET_ADMIN")
		}
	})
}

func assertAppErrorCode(t *testing.T, err error, want apperror.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s, got nil", want)
	}
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *apperror.Error (%s), got %T: %v", want, err, err)
	}
	if appErr.Code != want {
		t.Fatalf("expected error code %s, got %s (%s)", want, appErr.Code, appErr.Message)
	}
}
