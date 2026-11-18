package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatalf("Load(config.example.toml) failed: %v", err)
	}
	if cfg.Server.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", cfg.Server.ListenAddr)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 60*time.Second {
		t.Errorf("WriteTimeout = %v, want 60s", cfg.Server.WriteTimeout)
	}
	if cfg.Server.ContextTTL != 24*time.Hour {
		t.Errorf("ContextTTL = %v, want 24h", cfg.Server.ContextTTL)
	}
	if len(cfg.Server.ContextSignKey) < 32 {
		t.Errorf("ContextSignKey length = %d, want >= 32", len(cfg.Server.ContextSignKey))
	}
	if cfg.Database.MaxConnLifetime != 30*time.Minute {
		t.Errorf("MaxConnLifetime = %v, want 30m", cfg.Database.MaxConnLifetime)
	}
	if cfg.Database.LockTimeout != 3*time.Second {
		t.Errorf("LockTimeout = %v, want 3s", cfg.Database.LockTimeout)
	}
	if cfg.Limits.IdempotencyTTL != 72*time.Hour {
		t.Errorf("IdempotencyTTL = %v, want 72h", cfg.Limits.IdempotencyTTL)
	}
	if cfg.Export.Interval != 5*time.Minute {
		t.Errorf("Export.Interval = %v, want 5m", cfg.Export.Interval)
	}
}

func TestLoadTOMLDurationsAndDefaults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	content := `
[server]
read_timeout = "45s"
write_timeout = 90000000000

[database]
lock_timeout = "1500ms"

[export]
interval = "10m"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Server.ReadTimeout != 45*time.Second {
		t.Errorf("ReadTimeout = %v, want 45s", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 90*time.Second {
		t.Errorf("WriteTimeout = %v, want 90s", cfg.Server.WriteTimeout)
	}
	if cfg.Server.ContextTTL != 24*time.Hour {
		t.Errorf("ContextTTL = %v, want default 24h", cfg.Server.ContextTTL)
	}
	if cfg.Database.LockTimeout != 1500*time.Millisecond {
		t.Errorf("LockTimeout = %v, want 1500ms", cfg.Database.LockTimeout)
	}
	if cfg.Export.Interval != 10*time.Minute {
		t.Errorf("Export.Interval = %v, want 10m", cfg.Export.Interval)
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.toml")
	content := `
[server]
read_timeout = "not-a-duration"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
	if !strings.Contains(err.Error(), "server.read_timeout") {
		t.Errorf("error %q should mention server.read_timeout", err.Error())
	}
}

func TestLoadRejectsPlaceholderSignKey(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "placeholder.toml")
	content := `
[server]
context_sign_key = "replace-with-at-least-32-byte-hmac-secret-key"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for placeholder context_sign_key, got nil")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadEmptyEnvSignKeyKeepsRandomDefault(t *testing.T) {
	t.Setenv("MEMORY_CONTEXT_SIGN_KEY", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") failed with empty MEMORY_CONTEXT_SIGN_KEY: %v", err)
	}
	if len(cfg.Server.ContextSignKey) < 32 {
		t.Errorf("ContextSignKey length = %d, want >= 32", len(cfg.Server.ContextSignKey))
	}
}
