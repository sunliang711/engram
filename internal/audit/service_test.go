package audit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"
	"engram/internal/security"

	"github.com/google/uuid"
)

func TestSanitizeDetailsRedactsSecretsAndTruncates(t *testing.T) {
	long := strings.Repeat("a", 2000)
	got := SanitizeDetails(map[string]any{
		"password":        "super-secret",
		"passphrase":      "my-pass",
		"raw_token":       "aim_12345",
		"project_context": "ctx_abc",
		"token_id":        "tok-1",
		"token_prefix":    "aim_1234",
		"nested": map[string]any{
			"api_token": "aim_hidden",
			"title":     long,
		},
	})

	if got["password"] != "[REDACTED]" || got["passphrase"] != "[REDACTED]" || got["raw_token"] != "[REDACTED]" || got["project_context"] != "[REDACTED]" {
		t.Fatalf("expected sensitive keys to be redacted, got %+v", got)
	}
	if got["token_id"] != "tok-1" || got["token_prefix"] != "aim_1234" {
		t.Fatalf("expected safe token metadata to remain, got %+v", got)
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map, got %T", got["nested"])
	}
	if nested["api_token"] != "[REDACTED]" {
		t.Fatalf("expected nested api_token to be redacted, got %+v", nested)
	}
	if title, _ := nested["title"].(string); len(title) != 1024 {
		t.Fatalf("expected truncated length 1024, got %d", len(title))
	}
}

func setupAuditTestDB(t *testing.T) *db.Database {
	t.Helper()
	dsn := os.Getenv("ENGRAM_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_TEST_DSN is required for audit PostgreSQL tests")
	}
	ctx := context.Background()
	cfg := config.Default().Database
	cfg.DSN = dsn
	cfg.AutoMigrate = true
	database, err := db.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}

func TestAuditRecordQueryCleanupAndTasks(t *testing.T) {
	database := setupAuditTestDB(t)
	ctx := context.Background()
	metrics := observability.NewMetrics()
	svc := NewService(database.Pool, config.AuditConfig{
		Enabled:   true,
		Retention: 7 * 24 * time.Hour,
		LogReads:  true,
	}, metrics, observability.NewLogger("error", nil))

	tenantID := uuid.New()
	principalID := uuid.New()
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenantID, "audit-test-"+tenantID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO principals (id, identity_provider, subject, display_name) VALUES ($1, 'api_token', $2, 'Audit Tester')`, principalID, "audit-sub-"+principalID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO tenant_members (tenant_id, principal_id, role) VALUES ($1, $2, 'member')`, tenantID, principalID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE tenant_id = $1`, tenantID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_task_runs WHERE tenant_id = $1`, tenantID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM api_tokens WHERE tenant_id = $1`, tenantID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM memories WHERE tenant_id = $1`, tenantID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM tenant_members WHERE tenant_id = $1`, tenantID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM principals WHERE id = $1`, principalID)
	})

	now := time.Now().UTC()
	oldTime := now.Add(-10 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		svc.Record(ctx, Entry{
			TenantID:     &tenantID,
			ActorType:    ActorPrincipal,
			ActorID:      principalID.String(),
			ActorName:    "Audit Tester",
			AgentName:    "Codex",
			Source:       SourceMCP,
			Action:       "mcp.memory_create",
			ResourceType: "memory",
			ResourceID:   uuid.NewString(),
			Status:       StatusSuccess,
			DurationMS:   12,
			Details:      map[string]any{"title": "old", "password": "secret"},
			CreatedAt:    oldTime.Add(time.Duration(i) * time.Minute),
		})
	}
	svc.Record(ctx, Entry{
		TenantID:     &tenantID,
		ActorType:    ActorAdmin,
		ActorName:    "admin",
		Source:       SourceAdminUI,
		Action:       "admin.login",
		ResourceType: "admin_session",
		Status:       StatusDenied,
		ErrorCode:    "UNAUTHENTICATED",
		ErrorMessage: "bad password",
		CreatedAt:    now,
	})

	res, err := svc.QueryLogs(ctx, QueryFilter{TenantID: &tenantID, Source: SourceMCP, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Items) != 5 {
		t.Fatalf("expected 5 MCP logs, got total=%d len=%d", res.Total, len(res.Items))
	}
	if res.Items[0].Details["password"] != "[REDACTED]" {
		t.Fatalf("expected password redacted in stored entry, got %+v", res.Items[0].Details)
	}

	gotOne, err := svc.GetLog(ctx, res.Items[0].ID)
	if err != nil || gotOne.ID != res.Items[0].ID {
		t.Fatalf("GetLog failed: %v", err)
	}

	// Clean up logs older than 24h in batches of 2
	deleted, _, err := svc.CleanupExpired(ctx, 24*time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 5 {
		t.Fatalf("expected >= 5 deleted logs, got %d", deleted)
	}

	afterMCP, err := svc.QueryLogs(ctx, QueryFilter{TenantID: &tenantID, Source: SourceMCP})
	if err != nil || afterMCP.Total != 0 {
		t.Fatalf("expected 0 remaining old MCP logs, got total=%d err=%v", afterMCP.Total, err)
	}
	afterAdmin, err := svc.QueryLogs(ctx, QueryFilter{TenantID: &tenantID, Source: SourceAdminUI})
	if err != nil || afterAdmin.Total != 1 {
		t.Fatalf("expected recent admin log to remain, got total=%d err=%v", afterAdmin.Total, err)
	}

	// Seed findings for audit tasks
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO api_tokens (id, token_hash, token_prefix, tenant_id, principal_id, name, expires_at)
		VALUES ($1, $2, 'aim_expired', $3, $4, 'expired-key', now() - interval '2 days')
	`, uuid.New(), "hash-"+uuid.NewString(), tenantID, principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `
		INSERT INTO memories (id, tenant_id, scope, category, title, content_md, content_hash, created_by, updated_by)
		VALUES ($1, $2, 'global', 'notes', 'Bad hash', 'actual body', 'wrong-hash', $3, $3),
		       ($4, $2, 'global', 'notes', 'Good hash', 'valid body', $5, $3, $3)
	`, uuid.New(), tenantID, principalID, uuid.New(), security.ContentHash("valid body")); err != nil {
		t.Fatal(err)
	}

	credRun, err := svc.RunTask(ctx, TaskCredentialHygiene, &tenantID, "admin", "manual")
	if err != nil || credRun.Status != "success" || credRun.FindingsCount == 0 {
		t.Fatalf("credential_hygiene task failed: %+v, err=%v", credRun, err)
	}
	secRun, err := svc.RunTask(ctx, TaskSecurityEvents, &tenantID, "admin", "manual")
	if err != nil || secRun.Status != "success" || secRun.FindingsCount == 0 {
		t.Fatalf("security_events task failed: %+v, err=%v", secRun, err)
	}
	intRun, err := svc.RunTask(ctx, TaskDataIntegrity, &tenantID, "admin", "manual")
	if err != nil || intRun.Status != "success" || intRun.FindingsCount == 0 {
		t.Fatalf("data_integrity task failed: %+v, err=%v", intRun, err)
	}
	foundMismatch := false
	for _, f := range intRun.Findings {
		if f.Code == "MEMORY_HASH_MISMATCH" && f.Count == 1 {
			foundMismatch = true
		}
	}
	if !foundMismatch {
		t.Fatalf("expected MEMORY_HASH_MISMATCH finding, got %+v", intRun.Findings)
	}

	runs, totalRuns, err := svc.ListTaskRuns(ctx, "", 1, 20)
	if err != nil || totalRuns < 3 || len(runs) < 3 {
		t.Fatalf("ListTaskRuns failed: len=%d total=%d err=%v", len(runs), totalRuns, err)
	}
}
