package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"

	"github.com/google/uuid"
)

func TestConsoleRolePermissions(t *testing.T) {
	for _, tc := range []struct {
		role, method, path string
		want               bool
	}{
		{"admin", "POST", "/api/admin/tenants", true},
		{"viewer", "GET", "/api/admin/tenants", true},
		{"viewer", "POST", "/api/admin/tenants", false},
		{"viewer", "DELETE", "/api/admin/memories/delete", false},
		{"viewer", "POST", "/api/admin/tokens/reveal", false},
		{"viewer", "GET", "/api/admin/export/archive", false},
		{"viewer", "GET", "/api/admin/accounts", false},
		{"viewer", "POST", "/api/admin/accounts/x/role", false},
		{"viewer", "GET", "/api/admin/sessions", true},
		{"viewer", "DELETE", "/api/admin/sessions/x", true},
		{"other", "GET", "/api/admin/tenants", false},
	} {
		t.Run(tc.role+tc.method+tc.path, func(t *testing.T) {
			if got := canAccessConsole(tc.role, tc.method, tc.path); got != tc.want {
				t.Fatalf("allowed=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestAdminSessionTokensAreUnique(t *testing.T) {
	mgr := NewAuthManager(nil, "test-session-key", time.Hour, false)
	a, err := mgr.signSession("admin", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.signSession("admin", 1)
	if err != nil || a == b {
		t.Fatalf("independent logins must have unique tokens: %v", err)
	}
	if _, err := mgr.verifySessionToken(a + "bad"); err == nil {
		t.Fatal("tampered token accepted")
	}
	for _, claims := range []AdminSessionClaims{
		{Username: "admin", PasswordVersion: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()},
		{SessionID: uuid.NewString(), Username: "admin", PasswordVersion: 1, ExpiresAt: time.Now().Unix()},
	} {
		raw, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		payload := base64.RawURLEncoding.EncodeToString(raw)
		mac := hmac.New(sha256.New, mgr.signKey)
		mac.Write([]byte(payload))
		token := "adm_" + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if _, err := mgr.verifySessionToken(token); err == nil {
			t.Fatal("signed legacy or expired token accepted")
		}
	}
}

// 该集成测试只使用显式指定的专用临时数据库，覆盖完整 HTTP 与持久化行为。
func TestConsoleAccountsAndSessions(t *testing.T) {
	dsn := os.Getenv("ENGRAM_ACCOUNTS_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_ACCOUNTS_TEST_DSN is required for console account integration tests")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	pool := database.Pool
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_accounts`).Scan(&existing); err != nil || existing != 0 {
		t.Fatalf("dedicated test database must contain no accounts: count=%d err=%v", existing, err)
	}
	mgr := NewAuthManager(pool, "console-account-integration-key", time.Hour, false)
	h := NewHandler(pool, mgr, nil, nil, observability.NewLogger("error", nil))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	const password = "Test-Password-2026!"
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	adminID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts(id, username, password_hash, must_change_password) VALUES ($1, 'test-admin', $2, false)`, adminID, hash); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM admin_accounts WHERE username IN ('test-admin', 'test-viewer', 'test-second-admin')`); err != nil {
			t.Errorf("clean account fixtures: %v", err)
		}
	})
	request := func(token, method, path string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
		}
		return rec
	}
	login := func(username string) (string, AdminState) {
		t.Helper()
		token, state, err := mgr.Login(ctx, username, password, SessionMetadata{ClientIP: "192.0.2.10", UserAgent: "test-browser"})
		if err != nil {
			t.Fatal(err)
		}
		return token, state
	}
	adminToken, adminState := login("test-admin")
	otherToken, otherState := login("test-admin")
	var viewerID string
	var viewerToken string
	var viewerState AdminState
	t.Run("independent sessions, metadata, touch and persistence", func(t *testing.T) {
		if adminToken == otherToken {
			t.Fatal("same-second logins share a token")
		}
		request("", "GET", "/api/admin/sessions", nil, 401)
		rec := request(adminToken, "GET", "/api/admin/sessions", nil, 200)
		var result struct {
			Sessions []sessionView `json:"sessions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || len(result.Sessions) != 2 {
			t.Fatalf("session result: %s err=%v", rec.Body.String(), err)
		}
		if !result.Sessions[0].Current || result.Sessions[0].ClientIP != "192.0.2.10" || result.Sessions[0].UserAgent != "test-browser" {
			t.Fatalf("incorrect current session metadata: %+v", result.Sessions[0])
		}
		if strings.Contains(rec.Body.String(), "adm_") || strings.Contains(rec.Body.String(), "password_version") {
			t.Fatal("session API leaks credentials")
		}
		if _, err := pool.Exec(ctx, `UPDATE admin_sessions SET last_seen_at = now() - interval '5 minutes' WHERE id = $1`, adminState.SessionID); err != nil {
			t.Fatal(err)
		}
		restarted := NewAuthManager(pool, "console-account-integration-key", time.Hour, false)
		if _, err := restarted.VerifySession(ctx, adminToken); err != nil {
			t.Fatalf("session did not survive manager restart: %v", err)
		}
		var recent bool
		if err := pool.QueryRow(ctx, `SELECT last_seen_at > now() - interval '1 minute' FROM admin_sessions WHERE id = $1`, adminState.SessionID).Scan(&recent); err != nil || !recent {
			t.Fatalf("activity time not updated: %v", err)
		}
	})
	t.Run("create accounts with password verification and uniqueness", func(t *testing.T) {
		input := accountChange{Username: "test-viewer", Password: password, Role: "viewer", CurrentPassword: "wrong"}
		request(adminToken, "POST", "/api/admin/accounts", input, 400)
		input.CurrentPassword = password
		rec := request(adminToken, "POST", "/api/admin/accounts", input, 201)
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		viewerID = created.ID
		request(adminToken, "POST", "/api/admin/accounts", input, 409)
		viewerToken, viewerState = login("test-viewer")
		if viewerState.Role != "viewer" {
			t.Fatal("incorrect viewer role")
		}
		rec = request(adminToken, "GET", "/api/admin/accounts", nil, 200)
		if strings.Contains(rec.Body.String(), "password_hash") || strings.Contains(rec.Body.String(), "$2a$") {
			t.Fatal("account API leaks password hashes")
		}
	})
	t.Run("read-only boundaries, CSRF and session ownership", func(t *testing.T) {
		request(viewerToken, "GET", "/api/admin/tenants", nil, 200)
		for _, path := range []string{"/api/admin/tenants", "/api/admin/tokens/reveal", "/api/admin/integrations/safekeep/reveal", "/api/admin/accounts"} {
			request(viewerToken, "POST", path, map[string]string{}, 403)
		}
		request(viewerToken, "GET", "/api/admin/accounts", nil, 403)
		request(viewerToken, "GET", "/api/admin/export/archive", nil, 403)
		request(viewerToken, "DELETE", "/api/admin/sessions/"+adminState.SessionID, nil, 204)
		if _, err := mgr.VerifySession(ctx, adminToken); err != nil {
			t.Fatalf("viewer revoked another account's session: %v", err)
		}
		req := httptest.NewRequest("DELETE", "/api/admin/sessions/"+adminState.SessionID, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: adminToken})
		req.Header.Set("Origin", "https://other.example")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("cross-origin revoke=%d", rec.Code)
		}
		request(adminToken, "DELETE", "/api/admin/sessions/not-an-id", nil, 400)
	})
	t.Run("revoke another device and logout revoke server credentials", func(t *testing.T) {
		request(adminToken, "DELETE", "/api/admin/sessions/"+otherState.SessionID, nil, 204)
		request(adminToken, "DELETE", "/api/admin/sessions/"+otherState.SessionID, nil, 204)
		request(otherToken, "GET", "/api/admin/sessions", nil, 401)
		request(viewerToken, "POST", "/api/admin/logout", nil, 200)
		request(viewerToken, "GET", "/api/admin/sessions", nil, 401)
		viewerToken, viewerState = login("test-viewer")
	})
	t.Run("role, disable, enable and reset immediately invalidate sessions", func(t *testing.T) {
		if _, err := mgr.ChangeAccount(ctx, adminState, viewerID, "role", accountChange{Role: "admin", CurrentPassword: password}); err != nil {
			t.Fatal(err)
		}
		request(viewerToken, "GET", "/api/admin/sessions", nil, 401)
		viewerToken, viewerState = login("test-viewer")
		if viewerState.Role != "admin" {
			t.Fatal("role change not applied")
		}
		if _, err := mgr.ChangeAccount(ctx, adminState, viewerID, "disable", accountChange{CurrentPassword: password}); err != nil {
			t.Fatal(err)
		}
		request(viewerToken, "GET", "/api/admin/sessions", nil, 401)
		if _, _, err := mgr.Login(ctx, "test-viewer", password); err == nil {
			t.Fatal("disabled account signed in")
		}
		if _, err := mgr.ChangeAccount(ctx, adminState, viewerID, "enable", accountChange{CurrentPassword: password}); err != nil {
			t.Fatal(err)
		}
		request(viewerToken, "GET", "/api/admin/sessions", nil, 401)
		viewerToken, _ = login("test-viewer")
		if _, err := mgr.ChangeAccount(ctx, adminState, viewerID, "reset-password", accountChange{NewPassword: "Replacement-2026!", CurrentPassword: password}); err != nil {
			t.Fatal(err)
		}
		request(viewerToken, "GET", "/api/admin/sessions", nil, 401)
		if _, _, err := mgr.Login(ctx, "test-viewer", password); err == nil {
			t.Fatal("old password accepted after reset")
		}
		if _, err := mgr.ChangeAccount(ctx, adminState, viewerID, "role", accountChange{Role: "viewer", CurrentPassword: password}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("last administrator, self-disable and concurrent demotion", func(t *testing.T) {
		if _, err := mgr.ChangeAccount(ctx, adminState, adminID, "disable", accountChange{CurrentPassword: password}); err == nil {
			t.Fatal("self-disable accepted")
		}
		if _, err := mgr.ChangeAccount(ctx, adminState, adminID, "role", accountChange{Role: "viewer", CurrentPassword: password}); err == nil {
			t.Fatal("last administrator demotion accepted")
		}
		secondID, err := mgr.ChangeAccount(ctx, adminState, "", "create", accountChange{Username: "test-second-admin", Password: password, Role: "admin", CurrentPassword: password})
		if err != nil {
			t.Fatal(err)
		}
		_, second := login("test-second-admin")
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for id, actor := range map[string]AdminState{adminID: adminState, secondID: second} {
			wg.Add(1)
			go func(id string, actor AdminState) {
				defer wg.Done()
				_, err := mgr.ChangeAccount(ctx, actor, id, "role", accountChange{Role: "viewer", CurrentPassword: password})
				results <- err
			}(id, actor)
		}
		wg.Wait()
		close(results)
		succeeded := 0
		for err := range results {
			if err == nil {
				succeeded++
			}
		}
		if succeeded != 1 {
			t.Fatalf("concurrent demotions succeeded=%d, want 1", succeeded)
		}
	})
	t.Run("read-only account can change its own password", func(t *testing.T) {
		token, _, err := mgr.Login(ctx, "test-viewer", "Replacement-2026!")
		if err != nil {
			t.Fatal(err)
		}
		rec := request(token, "POST", "/api/admin/change-password", map[string]string{"current_password": "Replacement-2026!", "new_password": "Viewer-Updated-2026!"}, 200)
		if len(rec.Result().Cookies()) != 1 {
			t.Fatal("password change did not issue a fresh session")
		}
		request(token, "GET", "/api/admin/sessions", nil, 401)
		request(rec.Result().Cookies()[0].Value, "GET", "/api/admin/sessions", nil, 200)
	})
	t.Run("password change, expiry and revoking current session", func(t *testing.T) {
		token, _ := login("test-admin")
		stale, _ := login("test-admin")
		newToken, _, err := mgr.ChangePassword(ctx, "test-admin", password, "Updated-Password-2026!")
		if err != nil {
			t.Fatal(err)
		}
		request(stale, "GET", "/api/admin/sessions", nil, 401)
		request(token, "GET", "/api/admin/sessions", nil, 401)
		state, err := mgr.VerifySession(ctx, newToken)
		if err != nil {
			t.Fatal(err)
		}
		rec := request(newToken, "DELETE", "/api/admin/sessions/"+state.SessionID, nil, 204)
		if len(rec.Result().Cookies()) != 1 || rec.Result().Cookies()[0].MaxAge != -1 {
			t.Fatal("current session cookie not cleared")
		}
		request(newToken, "GET", "/api/admin/sessions", nil, 401)
		token, state, err = mgr.Login(ctx, "test-admin", "Updated-Password-2026!")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE admin_sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, state.SessionID); err != nil {
			t.Fatal(err)
		}
		request(token, "GET", "/api/admin/sessions", nil, 401)
	})
}
