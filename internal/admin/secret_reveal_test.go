package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"engram/internal/observability"
)

// TestSecretRevealRequiresAdminAndSameOrigin verifies secrets use existing management protections.
func TestSecretRevealRequiresAdminAndSameOrigin(t *testing.T) {
	mgr := NewAuthManager(nil, "test-secret-reveal-signing-key", time.Hour, false)
	h := NewHandler(nil, mgr, nil, nil, observability.NewLogger("error", nil))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, path := range []string{"/api/admin/integrations/safekeep/reveal", "/api/admin/tokens/reveal"} {
		for _, crossSite := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"field":"api_key"}`))
			req.Header.Set("Content-Type", "application/json")
			want := http.StatusUnauthorized
			if crossSite {
				req.Header.Set("Origin", "https://other.example.com")
				want = http.StatusForbidden
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Fatalf("%s cross-site=%v status=%d, want %d", path, crossSite, rec.Code, want)
			}
		}
	}
	rec := httptest.NewRecorder()
	h.handleSafekeepReveal(rec, httptest.NewRequest(http.MethodGet, "/api/admin/integrations/safekeep/reveal", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET reveal status=%d", rec.Code)
	}
}
