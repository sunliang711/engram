package safekeep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"
)

type testSealer struct{}

func (testSealer) SealToken(raw string) (string, error) {
	return "sealed:" + raw, nil
}

func (testSealer) UnsealToken(sealed string) (string, error) {
	return strings.TrimPrefix(sealed, "sealed:"), nil
}

func TestValidateBaseURLAndMaskKey(t *testing.T) {
	u, err := ValidateBaseURL("https://safekeep.local:8333/")
	if err != nil || u != "https://safekeep.local:8333" {
		t.Fatalf("unexpected ValidateBaseURL: %q, %v", u, err)
	}
	for _, bad := range []string{"ftp://example.com", "https://user:pass@example.com", "not-a-url"} {
		if _, err := ValidateBaseURL(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
	if got := maskKey("sk_1234567890abcdef"); got != "sk_12345****cdef" {
		t.Fatalf("maskKey = %q", got)
	}
}

func TestClientListUpstreamsAndUploadBackup(t *testing.T) {
	var gotKey, gotSHA, gotIdemp, gotQuery, gotMeta, gotFilename string
	var gotFileBytes []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/upstreams" && r.Method == http.MethodGet {
			if r.Header.Get("X-API-Key") != "sk_test_secret" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad api key"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"u1","name":"prod-s3","provider":"s3","enabled":true}]}`))
			return
		}
		if r.URL.Path == "/api/v1/backups" && r.Method == http.MethodPost {
			gotKey = r.Header.Get("X-API-Key")
			gotSHA = r.Header.Get("X-Content-SHA256")
			gotIdemp = r.Header.Get("Idempotency-Key")
			gotQuery = r.URL.RawQuery
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Fatal(err)
			}
			gotMeta = r.FormValue("metadata")
			f, hdr, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			gotFilename = hdr.Filename
			gotFileBytes, _ = io.ReadAll(f)

			_, _ = w.Write([]byte(`{"task":{"id":"task-123","status":"completed","original_filename":"` + hdr.Filename + `","file_size":11,"sha256":"abc","mode":"sync","deduplicated":false}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewClient()
	ctx := context.Background()

	upstreams, _, err := client.ListUpstreams(ctx, srv.URL, "sk_test_secret")
	if err != nil || len(upstreams) != 1 || upstreams[0].Name != "prod-s3" {
		t.Fatalf("ListUpstreams failed: %+v, err=%v", upstreams, err)
	}
	if _, _, err := client.ListUpstreams(ctx, srv.URL, "wrong"); err == nil {
		t.Fatal("expected auth error on wrong api key")
	}

	payload := []byte("hello-zip-1")
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])

	res, err := client.UploadBackup(ctx, srv.URL, "sk_test_secret", "snapshot.zip", payload, UploadOptions{
		Mode:           "sync",
		Path:           "engram/daily",
		Targets:        "prod-s3",
		IdempotencyKey: "idemp-1",
		Metadata:       map[string]any{"source": "engram"},
	})
	if err != nil {
		t.Fatalf("UploadBackup failed: %v", err)
	}
	if res.TaskID != "task-123" || res.Status != "completed" {
		t.Fatalf("unexpected upload result: %+v", res)
	}
	if gotKey != "sk_test_secret" || gotSHA != wantSHA || gotIdemp != "idemp-1" || gotFilename != "snapshot.zip" || string(gotFileBytes) != string(payload) {
		t.Fatalf("upload headers/body mismatch: key=%q sha=%q idemp=%q file=%q", gotKey, gotSHA, gotIdemp, gotFilename)
	}
	if !strings.Contains(gotQuery, "mode=sync") || !strings.Contains(gotQuery, "targets=prod-s3") || !strings.Contains(gotMeta, "engram") {
		t.Fatalf("upload query/meta mismatch: query=%q meta=%q", gotQuery, gotMeta)
	}
}

func TestServicePersistAndMaskCredentials(t *testing.T) {
	dsn := os.Getenv("ENGRAM_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_TEST_DSN is required for safekeep PostgreSQL tests")
	}
	ctx := context.Background()
	dbCfg := config.Default().Database
	dbCfg.DSN = dsn
	dbCfg.AutoMigrate = true
	database, err := db.Open(ctx, dbCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/upstreams" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []UpstreamInfo{{ID: "1", Name: "s3", Provider: "s3", Enabled: true}}})
			return
		}
		_, _ = w.Write([]byte(`{"task":{"id":"t1","status":"completed","mode":"sync"}}`))
	}))
	defer srv.Close()

	svc := NewService(database.Pool, testSealer{}, observability.NewLogger("error", nil))
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM safekeep_settings`)
	})

	enabled := true
	url := srv.URL
	key := "sk_live_1234567890"
	pass := "archive-passphrase"
	view, err := svc.UpdateSettings(ctx, UpdateSettingsRequest{
		Enabled:          &enabled,
		BaseURL:          &url,
		APIKey:           &key,
		BackupPassphrase: &pass,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || !view.HasAPIKey || !view.HasBackupPassphrase || strings.Contains(view.APIKeyMasked, "7890") == false || view.APIKeyMasked == key {
		t.Fatalf("unexpected settings view: %+v", view)
	}

	testRes, err := svc.TestConnection(ctx, TestRequest{})
	if err != nil || !testRes.OK || len(testRes.Upstreams) != 1 {
		t.Fatalf("TestConnection failed: %+v, err=%v", testRes, err)
	}
	gotPass, err := svc.BackupPassphrase(ctx)
	if err != nil || gotPass != pass {
		t.Fatalf("BackupPassphrase = %q, %v", gotPass, err)
	}
	upRes, err := svc.UploadArchive(ctx, "snap.zip", []byte("zipdata"), UploadOptions{})
	if err != nil || upRes.TaskID != "t1" {
		t.Fatalf("UploadArchive failed: %+v, err=%v", upRes, err)
	}
}
