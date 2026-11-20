package safekeep

import "time"

// Sealer encrypts and decrypts stored credentials using Engram's server signing key.
type Sealer interface {
	SealToken(raw string) (string, error)
	UnsealToken(sealed string) (string, error)
}

// UpstreamInfo describes an upstream storage target returned by SafeKeep's /api/v1/upstreams endpoint.
type UpstreamInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
}

// SettingsView is the public representation of SafeKeep integration settings (secrets are masked).
type SettingsView struct {
	BaseURL             string     `json:"base_url"`
	HasAPIKey           bool       `json:"has_api_key"`
	APIKeyMasked        string     `json:"api_key_masked"`
	HasBackupPassphrase bool       `json:"has_backup_passphrase"`
	DefaultMode         string     `json:"default_mode"`
	DefaultPath         string     `json:"default_path"`
	DefaultTargets      string     `json:"default_targets"`
	LastTestedAt        *time.Time `json:"last_tested_at,omitempty"`
	LastTestStatus      string     `json:"last_test_status"`
	LastTestError       string     `json:"last_test_error"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// UpdateSettingsRequest updates persisted SafeKeep settings.
type UpdateSettingsRequest struct {
	BaseURL               *string `json:"base_url,omitempty"`
	APIKey                *string `json:"api_key,omitempty"`
	ClearAPIKey           bool    `json:"clear_api_key,omitempty"`
	BackupPassphrase      *string `json:"backup_passphrase,omitempty"`
	ClearBackupPassphrase bool    `json:"clear_backup_passphrase,omitempty"`
	DefaultMode           *string `json:"default_mode,omitempty"`
	DefaultPath           *string `json:"default_path,omitempty"`
	DefaultTargets        *string `json:"default_targets,omitempty"`
}

// TestRequest specifies optional override credentials to test SafeKeep connectivity.
type TestRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// TestResult reports the outcome of testing a SafeKeep connection.
type TestResult struct {
	OK        bool           `json:"ok"`
	LatencyMS int64          `json:"latency_ms"`
	Upstreams []UpstreamInfo `json:"upstreams"`
	Message   string         `json:"message"`
}

// UploadOptions overrides SafeKeep upload parameters for a single backup run.
type UploadOptions struct {
	Mode           string
	Path           string
	Targets        string
	IdempotencyKey string
	Metadata       map[string]any
}

// UploadResult summarizes the backup task created in SafeKeep.
type UploadResult struct {
	TaskID           string `json:"task_id"`
	Status           string `json:"status"`
	OriginalFilename string `json:"original_filename"`
	FileSize         int64  `json:"file_size"`
	SHA256           string `json:"sha256"`
	Mode             string `json:"mode"`
	Deduplicated     bool   `json:"deduplicated,omitempty"`
	TimedOutToAsync  bool   `json:"timed_out_to_async,omitempty"`
	IdempotentReplay bool   `json:"idempotent_replay,omitempty"`
}
