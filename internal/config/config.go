package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// StaticTokenConfig defines a pre-configured API token in config file for bootstrapping or testing.
type StaticTokenConfig struct {
	Token       string `toml:"token" json:"token"`
	TenantID    string `toml:"tenant_id" json:"tenant_id"`
	PrincipalID string `toml:"principal_id" json:"principal_id"`
}

// Config holds the full configuration for the Engram Server.
type Config struct {
	Server    ServerConfig    `toml:"server" json:"server"`
	Database  DatabaseConfig  `toml:"database" json:"database"`
	Auth      AuthConfig      `toml:"auth" json:"auth"`
	Limits    LimitsConfig    `toml:"limits" json:"limits"`
	Export    ExportConfig    `toml:"export" json:"export"`
	Audit     AuditConfig     `toml:"audit" json:"audit"`
	Scheduler SchedulerConfig `toml:"scheduler" json:"scheduler"`
	Safekeep  SafekeepConfig  `toml:"safekeep" json:"safekeep"`
	Log       LogConfig       `toml:"log" json:"log"`
}

type ServerConfig struct {
	ListenAddr     string        `toml:"listen_addr" json:"listen_addr"`
	ReadTimeout    time.Duration `toml:"read_timeout" json:"read_timeout"`
	WriteTimeout   time.Duration `toml:"write_timeout" json:"write_timeout"`
	ContextSignKey string        `toml:"context_sign_key" json:"context_sign_key"`
	ContextTTL     time.Duration `toml:"context_ttl" json:"context_ttl"`
}

type DatabaseConfig struct {
	DSN             string        `toml:"dsn" json:"dsn"`
	MaxConns        int32         `toml:"max_conns" json:"max_conns"`
	MinConns        int32         `toml:"min_conns" json:"min_conns"`
	MaxConnLifetime time.Duration `toml:"max_conn_lifetime" json:"max_conn_lifetime"`
	LockTimeout     time.Duration `toml:"lock_timeout" json:"lock_timeout"`
	AutoMigrate     bool          `toml:"auto_migrate" json:"auto_migrate"`
}

type AuthConfig struct {
	EnableMTLSHeaders bool                `toml:"enable_mtls_headers" json:"enable_mtls_headers"`
	EnableOIDCHeaders bool                `toml:"enable_oidc_headers" json:"enable_oidc_headers"`
	ResetAdmin        bool                `toml:"reset_admin" json:"reset_admin"`
	CookieSecure      bool                `toml:"cookie_secure" json:"cookie_secure"`
	StaticTokens      []StaticTokenConfig `toml:"static_tokens" json:"static_tokens"`
}

type LimitsConfig struct {
	MaxTitleBytes      int           `toml:"max_title_bytes" json:"max_title_bytes"`
	MaxContentBytes    int           `toml:"max_content_bytes" json:"max_content_bytes"`
	MaxTags            int           `toml:"max_tags" json:"max_tags"`
	MaxTagBytes        int           `toml:"max_tag_bytes" json:"max_tag_bytes"`
	MaxSourceRefs      int           `toml:"max_source_refs" json:"max_source_refs"`
	MaxQueryLimit      int           `toml:"max_query_limit" json:"max_query_limit"`
	DefaultQueryLimit  int           `toml:"default_query_limit" json:"default_query_limit"`
	WriteRatePerMinute int           `toml:"write_rate_per_minute" json:"write_rate_per_minute"`
	WriteBurst         int           `toml:"write_burst" json:"write_burst"`
	IdempotencyTTL     time.Duration `toml:"idempotency_ttl" json:"idempotency_ttl"`
}

type ExportConfig struct {
	Enabled   bool          `toml:"enabled" json:"enabled"`
	OutputDir string        `toml:"output_dir" json:"output_dir"`
	Interval  time.Duration `toml:"interval" json:"interval"`
	GitCommit bool          `toml:"git_commit" json:"git_commit"`
}

type AuditConfig struct {
	Enabled   bool          `toml:"enabled" json:"enabled"`
	Retention time.Duration `toml:"retention" json:"retention"`
	LogReads  bool          `toml:"log_reads" json:"log_reads"`
}

type SchedulerConfig struct {
	Enabled               bool          `toml:"enabled" json:"enabled"`
	AuditCleanupEnabled   bool          `toml:"audit_cleanup_enabled" json:"audit_cleanup_enabled"`
	AuditCleanupInterval  time.Duration `toml:"audit_cleanup_interval" json:"audit_cleanup_interval"`
	AuditCleanupBatchSize int           `toml:"audit_cleanup_batch_size" json:"audit_cleanup_batch_size"`
	SafekeepBackupEnabled bool          `toml:"safekeep_backup_enabled" json:"safekeep_backup_enabled"`
	SafekeepBackupInterval time.Duration `toml:"safekeep_backup_interval" json:"safekeep_backup_interval"`
}

type SafekeepConfig struct {
	BaseURL        string `toml:"base_url" json:"base_url"`
	APIKey         string `toml:"api_key" json:"api_key"`
	DefaultMode    string `toml:"default_mode" json:"default_mode"`
	DefaultPath    string `toml:"default_path" json:"default_path"`
	DefaultTargets string `toml:"default_targets" json:"default_targets"`
}

type LogConfig struct {
	Level string `toml:"level" json:"level"`
}

func randomSignKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// Default returns a Config populated with safe V1 defaults.
func Default() Config {
	return Config{
		Server: ServerConfig{
			ListenAddr:     ":8080",
			ReadTimeout:    30 * time.Second,
			WriteTimeout:   60 * time.Second,
			ContextSignKey: randomSignKey(),
			ContextTTL:     24 * time.Hour,
		},
		Database: DatabaseConfig{
			DSN:             "postgres://postgres:postgres@localhost:5432/engram?sslmode=disable",
			MaxConns:        20,
			MinConns:        2,
			MaxConnLifetime: 30 * time.Minute,
			LockTimeout:     3 * time.Second,
			AutoMigrate:     true,
		},
		Auth: AuthConfig{
			EnableMTLSHeaders: false,
			EnableOIDCHeaders: false,
		},
		Limits: LimitsConfig{
			MaxTitleBytes:      256,
			MaxContentBytes:    64 * 1024,
			MaxTags:            20,
			MaxTagBytes:        64,
			MaxSourceRefs:      20,
			MaxQueryLimit:      100,
			DefaultQueryLimit:  20,
			WriteRatePerMinute: 120,
			WriteBurst:         30,
			IdempotencyTTL:     72 * time.Hour,
		},
		Export: ExportConfig{
			Enabled:   false,
			OutputDir: "./export",
			Interval:  5 * time.Minute,
			GitCommit: false,
		},
		Audit: AuditConfig{
			Enabled:   true,
			Retention: 30 * 24 * time.Hour,
			LogReads:  true,
		},
		Scheduler: SchedulerConfig{
			Enabled:                true,
			AuditCleanupEnabled:    true,
			AuditCleanupInterval:   1 * time.Hour,
			AuditCleanupBatchSize:  1000,
			SafekeepBackupEnabled:  false,
			SafekeepBackupInterval: 24 * time.Hour,
		},
		Safekeep: SafekeepConfig{
			DefaultMode: "sync",
			DefaultPath: "engram",
		},
		Log: LogConfig{
			Level: "info",
		},
	}
}

// Load loads configuration from an optional TOML file and overrides with environment variables.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config file %s: %w", path, err)
		}
		if err := unmarshalTOML(data, &cfg); err != nil {
			return cfg, fmt.Errorf("unmarshal config file %s: %w", path, err)
		}
	}
	applyEnvOverrides(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// unmarshalTOML decodes TOML bytes into cfg while supporting Go duration strings
// (e.g. "30s", "24h") for time.Duration fields.
func unmarshalTOML(data []byte, cfg *Config) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	if err := normalizeDurationFields(raw, reflect.TypeOf(*cfg), ""); err != nil {
		return err
	}
	normalized, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	return toml.Unmarshal(normalized, cfg)
}

var durationType = reflect.TypeOf(time.Duration(0))

func normalizeDurationFields(raw map[string]any, structType reflect.Type, prefix string) error {
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		key := strings.Split(field.Tag.Get("toml"), ",")[0]
		if key == "-" {
			continue
		}
		if key == "" {
			key = field.Name
		}
		val, exists := raw[key]
		if !exists {
			continue
		}
		fieldPath := key
		if prefix != "" {
			fieldPath = prefix + "." + key
		}

		ft := field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		switch {
		case ft == durationType:
			s, ok := val.(string)
			if !ok {
				continue
			}
			d, err := ParseDuration(s)
			if err != nil {
				return fmt.Errorf("invalid duration for %s (%q): %w", fieldPath, s, err)
			}
			raw[key] = int64(d)
		case ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Time{}):
			if subMap, ok := val.(map[string]any); ok {
				if err := normalizeDurationFields(subMap, ft, fieldPath); err != nil {
					return err
				}
			}
		case ft.Kind() == reflect.Slice:
			elemType := ft.Elem()
			if elemType.Kind() == reflect.Pointer {
				elemType = elemType.Elem()
			}
			if elemType.Kind() == reflect.Struct {
				if items, ok := val.([]any); ok {
					for idx, item := range items {
						if subMap, ok := item.(map[string]any); ok {
							if err := normalizeDurationFields(subMap, elemType, fmt.Sprintf("%s[%d]", fieldPath, idx)); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// applyEnvOverrides applies supported environment variables to the loaded config.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("ENGRAM_LISTEN_ADDR"); v != "" {
		cfg.Server.ListenAddr = v
	}
	if v := os.Getenv("ENGRAM_CONTEXT_SIGN_KEY"); v != "" {
		cfg.Server.ContextSignKey = v
	}
	if v := os.Getenv("ENGRAM_DATABASE_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := os.Getenv("ENGRAM_EXPORT_DIR"); v != "" {
		cfg.Export.OutputDir = v
	}
	if v := os.Getenv("ENGRAM_EXPORT_ENABLED"); v != "" {
		cfg.Export.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("ENGRAM_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("ENGRAM_WRITE_RATE_PER_MINUTE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Limits.WriteRatePerMinute = n
		}
	}
	if v := os.Getenv("ENGRAM_COOKIE_SECURE"); v != "" {
		cfg.Auth.CookieSecure = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("RESET_ADMIN"); v != "" {
		cfg.Auth.ResetAdmin = !strings.EqualFold(v, "false") && v != "0"
	} else if v := os.Getenv("reset_admin"); v != "" {
		cfg.Auth.ResetAdmin = !strings.EqualFold(v, "false") && v != "0"
	}
	if v := os.Getenv("ENGRAM_AUDIT_ENABLED"); v != "" {
		cfg.Audit.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("ENGRAM_AUDIT_RETENTION"); v != "" {
		if d, err := ParseDuration(v); err == nil && d > 0 {
			cfg.Audit.Retention = d
		}
	}
	if v := os.Getenv("ENGRAM_AUDIT_LOG_READS"); v != "" {
		cfg.Audit.LogReads = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("ENGRAM_SCHEDULER_ENABLED"); v != "" {
		cfg.Scheduler.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("ENGRAM_AUDIT_CLEANUP_ENABLED"); v != "" {
		cfg.Scheduler.AuditCleanupEnabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("ENGRAM_AUDIT_CLEANUP_INTERVAL"); v != "" {
		if d, err := ParseDuration(v); err == nil && d > 0 {
			cfg.Scheduler.AuditCleanupInterval = d
		}
	}
	if v := os.Getenv("ENGRAM_AUDIT_CLEANUP_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Scheduler.AuditCleanupBatchSize = n
		}
	}
	if v := os.Getenv("ENGRAM_SAFEKEEP_URL"); v != "" {
		cfg.Safekeep.BaseURL = strings.TrimSpace(v)
	}
	if v := os.Getenv("ENGRAM_SAFEKEEP_API_KEY"); v != "" {
		cfg.Safekeep.APIKey = strings.TrimSpace(v)
	}
	if v := os.Getenv("ENGRAM_SAFEKEEP_BACKUP_ENABLED"); v != "" {
		cfg.Scheduler.SafekeepBackupEnabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("ENGRAM_SAFEKEEP_BACKUP_INTERVAL"); v != "" {
		if d, err := ParseDuration(v); err == nil && d > 0 {
			cfg.Scheduler.SafekeepBackupInterval = d
		}
	}
}

// ParseDuration parses a Go duration string and additionally supports day units ("d" = 24h),
// such as "30d" or "7d12h".
func ParseDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	sign := time.Duration(1)
	rest := s
	if strings.HasPrefix(rest, "-") {
		sign = -1
		rest = rest[1:]
	} else if strings.HasPrefix(rest, "+") {
		rest = rest[1:]
	}
	idx := strings.IndexByte(rest, 'd')
	if idx <= 0 {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	days, err := strconv.ParseInt(rest[:idx], 10, 64)
	if err != nil || days < 0 || days > 36500 {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	total := time.Duration(days) * 24 * time.Hour
	rem := strings.TrimSpace(rest[idx+1:])
	if rem != "" {
		sub, err := time.ParseDuration(rem)
		if err != nil || sub < 0 {
			return 0, fmt.Errorf("invalid duration %q", raw)
		}
		total += sub
	}
	return sign * total, nil
}

// Validate checks that required configuration fields are valid and rejects placeholder signing keys.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("database.dsn is required")
	}
	key := strings.TrimSpace(c.Server.ContextSignKey)
	if len(key) < 32 {
		return fmt.Errorf("server.context_sign_key must be at least 32 bytes")
	}
	lowerKey := strings.ToLower(key)
	if strings.Contains(lowerKey, "change-me") || strings.Contains(lowerKey, "replace-with") || strings.Contains(lowerKey, "example") {
		return fmt.Errorf("server.context_sign_key must not use a placeholder or example value")
	}
	if c.Limits.MaxTitleBytes <= 0 || c.Limits.MaxContentBytes <= 0 {
		return fmt.Errorf("limits.max_title_bytes and limits.max_content_bytes must be positive")
	}
	if c.Audit.Retention <= 0 {
		return fmt.Errorf("audit.retention must be positive")
	}
	if c.Scheduler.AuditCleanupInterval <= 0 {
		return fmt.Errorf("scheduler.audit_cleanup_interval must be positive")
	}
	if c.Scheduler.AuditCleanupBatchSize <= 0 {
		return fmt.Errorf("scheduler.audit_cleanup_batch_size must be positive")
	}
	if c.Scheduler.SafekeepBackupInterval <= 0 {
		return fmt.Errorf("scheduler.safekeep_backup_interval must be positive")
	}
	return nil
}
