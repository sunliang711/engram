package config

import (
	"fmt"
	"os"
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

// Config holds the full configuration for the AI Shared Memory Server.
type Config struct {
	Server   ServerConfig   `toml:"server" json:"server"`
	Database DatabaseConfig `toml:"database" json:"database"`
	Auth     AuthConfig     `toml:"auth" json:"auth"`
	Limits   LimitsConfig   `toml:"limits" json:"limits"`
	Export   ExportConfig   `toml:"export" json:"export"`
	Log      LogConfig      `toml:"log" json:"log"`
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

type LogConfig struct {
	Level string `toml:"level" json:"level"`
}

// Default returns a Config populated with safe V1 defaults.
func Default() Config {
	return Config{
		Server: ServerConfig{
			ListenAddr:     ":8080",
			ReadTimeout:    30 * time.Second,
			WriteTimeout:   60 * time.Second,
			ContextSignKey: "change-me-in-production-hmac-secret-key-32bytes",
			ContextTTL:     24 * time.Hour,
		},
		Database: DatabaseConfig{
			DSN:             "postgres://postgres:postgres@localhost:5432/ai_memory?sslmode=disable",
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
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("unmarshal config file %s: %w", path, err)
		}
	}
	applyEnvOverrides(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("MEMORY_LISTEN_ADDR"); v != "" {
		cfg.Server.ListenAddr = v
	}
	if v := os.Getenv("MEMORY_CONTEXT_SIGN_KEY"); v != "" {
		cfg.Server.ContextSignKey = v
	}
	if v := os.Getenv("MEMORY_DATABASE_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := os.Getenv("MEMORY_EXPORT_DIR"); v != "" {
		cfg.Export.OutputDir = v
	}
	if v := os.Getenv("MEMORY_EXPORT_ENABLED"); v != "" {
		cfg.Export.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("MEMORY_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("MEMORY_WRITE_RATE_PER_MINUTE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Limits.WriteRatePerMinute = n
		}
	}
	if v := os.Getenv("RESET_ADMIN"); v != "" {
		cfg.Auth.ResetAdmin = !strings.EqualFold(v, "false") && v != "0"
	} else if v := os.Getenv("reset_admin"); v != "" {
		cfg.Auth.ResetAdmin = !strings.EqualFold(v, "false") && v != "0"
	}
}

// Validate checks that required configuration fields are valid.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("database.dsn is required")
	}
	if len(c.Server.ContextSignKey) < 16 {
		return fmt.Errorf("server.context_sign_key must be at least 16 bytes")
	}
	if c.Limits.MaxTitleBytes <= 0 || c.Limits.MaxContentBytes <= 0 {
		return fmt.Errorf("limits.max_title_bytes and limits.max_content_bytes must be positive")
	}
	return nil
}
