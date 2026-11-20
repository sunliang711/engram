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
	if !cfg.Audit.Enabled || cfg.Audit.Retention != 30*24*time.Hour || !cfg.Audit.LogReads {
		t.Errorf("Audit defaults mismatch: %+v", cfg.Audit)
	}
	if !cfg.Scheduler.Enabled || !cfg.Scheduler.AuditCleanupEnabled || cfg.Scheduler.AuditCleanupInterval != time.Hour || cfg.Scheduler.AuditCleanupBatchSize != 1000 {
		t.Errorf("Scheduler defaults mismatch: %+v", cfg.Scheduler)
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
	t.Setenv("ENGRAM_CONTEXT_SIGN_KEY", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") failed with empty ENGRAM_CONTEXT_SIGN_KEY: %v", err)
	}
	if len(cfg.Server.ContextSignKey) < 32 {
		t.Errorf("ContextSignKey length = %d, want >= 32", len(cfg.Server.ContextSignKey))
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	signKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("ENGRAM_LISTEN_ADDR", ":9090")
	t.Setenv("ENGRAM_CONTEXT_SIGN_KEY", signKey)
	t.Setenv("ENGRAM_DATABASE_DSN", "postgres://user:pass@db:5432/engram?sslmode=disable")
	t.Setenv("ENGRAM_EXPORT_DIR", "/tmp/engram-export")
	t.Setenv("ENGRAM_EXPORT_ENABLED", "true")
	t.Setenv("ENGRAM_LOG_LEVEL", "debug")
	t.Setenv("ENGRAM_WRITE_RATE_PER_MINUTE", "240")
	t.Setenv("ENGRAM_COOKIE_SECURE", "1")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") failed: %v", err)
	}
	if cfg.Server.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want :9090", cfg.Server.ListenAddr)
	}
	if cfg.Server.ContextSignKey != signKey {
		t.Errorf("ContextSignKey = %q, want %q", cfg.Server.ContextSignKey, signKey)
	}
	if cfg.Database.DSN != "postgres://user:pass@db:5432/engram?sslmode=disable" {
		t.Errorf("DSN = %q, want overridden DSN", cfg.Database.DSN)
	}
	if cfg.Export.OutputDir != "/tmp/engram-export" || !cfg.Export.Enabled {
		t.Errorf("Export = %+v, want enabled /tmp/engram-export", cfg.Export)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
	if cfg.Limits.WriteRatePerMinute != 240 {
		t.Errorf("WriteRatePerMinute = %d, want 240", cfg.Limits.WriteRatePerMinute)
	}
	if !cfg.Auth.CookieSecure {
		t.Errorf("CookieSecure = false, want true")
	}
}

func TestParseDurationAndAuditSchedulerOverrides(t *testing.T) {
	if d, err := ParseDuration("30d"); err != nil || d != 30*24*time.Hour {
		t.Fatalf("ParseDuration(30d) = %v, %v", d, err)
	}
	if d, err := ParseDuration("7d12h"); err != nil || d != 7*24*time.Hour+12*time.Hour {
		t.Fatalf("ParseDuration(7d12h) = %v, %v", d, err)
	}
	if _, err := ParseDuration("bad"); err == nil {
		t.Fatal("expected error for invalid duration")
	}

	t.Setenv("ENGRAM_AUDIT_ENABLED", "false")
	t.Setenv("ENGRAM_AUDIT_RETENTION", "14d")
	t.Setenv("ENGRAM_AUDIT_LOG_READS", "false")
	t.Setenv("ENGRAM_SCHEDULER_ENABLED", "false")
	t.Setenv("ENGRAM_AUDIT_CLEANUP_ENABLED", "false")
	t.Setenv("ENGRAM_AUDIT_CLEANUP_INTERVAL", "15m")
	t.Setenv("ENGRAM_AUDIT_CLEANUP_BATCH_SIZE", "500")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") failed: %v", err)
	}
	if cfg.Audit.Enabled || cfg.Audit.Retention != 14*24*time.Hour || cfg.Audit.LogReads {
		t.Errorf("Audit env override mismatch: %+v", cfg.Audit)
	}
	if cfg.Scheduler.Enabled || cfg.Scheduler.AuditCleanupEnabled || cfg.Scheduler.AuditCleanupInterval != 15*time.Minute || cfg.Scheduler.AuditCleanupBatchSize != 500 {
		t.Errorf("Scheduler env override mismatch: %+v", cfg.Scheduler)
	}
}
