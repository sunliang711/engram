package safekeep

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"engram/internal/apperror"
	"engram/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

const settingsRowID = "default"

// Service manages persisted SafeKeep settings, connectivity testing, and archive uploads.
type Service struct {
	pool   *pgxpool.Pool
	sealer Sealer
	client *Client
	log    zerolog.Logger
}

// NewService creates a SafeKeep integration Service.
func NewService(pool *pgxpool.Pool, sealer Sealer, log zerolog.Logger) *Service {
	return &Service{
		pool:   pool,
		sealer: sealer,
		client: NewClient(),
		log:    log,
	}
}

type storedSettings struct {
	baseURL            string
	apiKeyCipher       string
	apiKeyPrefix       string
	passphraseCipher   string
	hasPassphrase      bool
	defaultMode        string
	defaultPath        string
	defaultTargets     string
	lastTestedAt       *time.Time
	lastTestStatus     string
	lastTestError      string
	updatedAt          time.Time
}

// SeedDefaults inserts initial SafeKeep settings from static config when no row exists yet.
func (s *Service) SeedDefaults(ctx context.Context, cfg config.SafekeepConfig) error {
	if s == nil || s.pool == nil {
		return nil
	}
	cleanURL, err := ValidateBaseURL(cfg.BaseURL)
	if err != nil {
		return err
	}
	mode := normalizeUploadMode(cfg.DefaultMode)
	defaultPath := strings.TrimSpace(cfg.DefaultPath)
	if defaultPath == "" {
		defaultPath = "engram"
	}

	var apiKeyCipher, apiKeyPrefix string
	if rawKey := strings.TrimSpace(cfg.APIKey); rawKey != "" && s.sealer != nil {
		sealed, err := s.sealer.SealToken(rawKey)
		if err != nil {
			return fmt.Errorf("seal initial safekeep api key: %w", err)
		}
		apiKeyCipher = sealed
		apiKeyPrefix = maskKey(rawKey)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO safekeep_settings (
			id, base_url, api_key_ciphertext, api_key_prefix,
			default_mode, default_path, default_targets, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (id) DO NOTHING
	`, settingsRowID, cleanURL, apiKeyCipher, apiKeyPrefix, mode, defaultPath, strings.TrimSpace(cfg.DefaultTargets))
	if err != nil {
		return fmt.Errorf("seed safekeep settings: %w", err)
	}
	return nil
}

// loadSettings reads connection settings without the legacy integration switch.
func (s *Service) loadSettings(ctx context.Context) (storedSettings, error) {
	var st storedSettings
	if s == nil || s.pool == nil {
		st.defaultMode = "sync"
		st.defaultPath = "engram"
		return st, nil
	}
	err := s.pool.QueryRow(ctx, `
		SELECT base_url, api_key_ciphertext, api_key_prefix,
		       backup_passphrase_ciphertext, has_backup_passphrase,
		       default_mode, default_path, default_targets,
		       last_tested_at, last_test_status, last_test_error, updated_at
		FROM safekeep_settings
		WHERE id = $1
	`, settingsRowID).Scan(
		&st.baseURL, &st.apiKeyCipher, &st.apiKeyPrefix,
		&st.passphraseCipher, &st.hasPassphrase,
		&st.defaultMode, &st.defaultPath, &st.defaultTargets,
		&st.lastTestedAt, &st.lastTestStatus, &st.lastTestError, &st.updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		st.defaultMode = "sync"
		st.defaultPath = "engram"
		return st, nil
	}
	if err != nil {
		return st, apperror.Wrap(apperror.CodeInternalError, "load safekeep settings", err)
	}
	if st.defaultMode == "" {
		st.defaultMode = "sync"
	}
	return st, nil
}

// toSettingsView exposes connection settings while masking saved credentials.
func toSettingsView(st storedSettings) *SettingsView {
	return &SettingsView{
		BaseURL:             st.baseURL,
		HasAPIKey:           st.apiKeyCipher != "",
		APIKeyMasked:        st.apiKeyPrefix,
		HasBackupPassphrase: st.hasPassphrase,
		DefaultMode:         st.defaultMode,
		DefaultPath:         st.defaultPath,
		DefaultTargets:      st.defaultTargets,
		LastTestedAt:        st.lastTestedAt,
		LastTestStatus:      st.lastTestStatus,
		LastTestError:       st.lastTestError,
		UpdatedAt:           st.updatedAt,
	}
}

// GetSettings returns the current SafeKeep settings with secrets masked.
func (s *Service) GetSettings(ctx context.Context) (*SettingsView, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	return toSettingsView(st), nil
}

// UpdateSettings validates, seals secrets, and persists SafeKeep integration settings.
func (s *Service) UpdateSettings(ctx context.Context, req UpdateSettingsRequest) (*SettingsView, error) {
	if s == nil || s.pool == nil {
		return nil, apperror.New(apperror.CodeInternalError, "safekeep service not initialized")
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}

	if req.BaseURL != nil {
		cleanURL, err := ValidateBaseURL(*req.BaseURL)
		if err != nil {
			return nil, err
		}
		st.baseURL = cleanURL
	}
	if req.ClearAPIKey {
		st.apiKeyCipher = ""
		st.apiKeyPrefix = ""
	} else if req.APIKey != nil && strings.TrimSpace(*req.APIKey) != "" {
		if s.sealer == nil {
			return nil, apperror.New(apperror.CodeInternalError, "credential sealer not initialized")
		}
		rawKey := strings.TrimSpace(*req.APIKey)
		sealed, err := s.sealer.SealToken(rawKey)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "seal safekeep api key", err)
		}
		st.apiKeyCipher = sealed
		st.apiKeyPrefix = maskKey(rawKey)
	}
	if req.ClearBackupPassphrase {
		st.passphraseCipher = ""
		st.hasPassphrase = false
	} else if req.BackupPassphrase != nil && *req.BackupPassphrase != "" {
		if s.sealer == nil {
			return nil, apperror.New(apperror.CodeInternalError, "credential sealer not initialized")
		}
		sealed, err := s.sealer.SealToken(*req.BackupPassphrase)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInternalError, "seal backup passphrase", err)
		}
		st.passphraseCipher = sealed
		st.hasPassphrase = true
	}
	if req.DefaultMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*req.DefaultMode))
		if mode != "sync" && mode != "async" {
			return nil, apperror.New(apperror.CodeInvalidArgument, "default_mode must be 'sync' or 'async'")
		}
		st.defaultMode = mode
	}
	if req.DefaultPath != nil {
		st.defaultPath = strings.Trim(strings.TrimSpace(*req.DefaultPath), "/")
	}
	if req.DefaultTargets != nil {
		st.defaultTargets = strings.TrimSpace(*req.DefaultTargets)
	}

	now := time.Now().UTC()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO safekeep_settings (
			id, base_url, api_key_ciphertext, api_key_prefix,
			backup_passphrase_ciphertext, has_backup_passphrase,
			default_mode, default_path, default_targets, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET
			base_url = EXCLUDED.base_url,
			api_key_ciphertext = EXCLUDED.api_key_ciphertext,
			api_key_prefix = EXCLUDED.api_key_prefix,
			backup_passphrase_ciphertext = EXCLUDED.backup_passphrase_ciphertext,
			has_backup_passphrase = EXCLUDED.has_backup_passphrase,
			default_mode = EXCLUDED.default_mode,
			default_path = EXCLUDED.default_path,
			default_targets = EXCLUDED.default_targets,
			updated_at = EXCLUDED.updated_at
	`, settingsRowID, st.baseURL, st.apiKeyCipher, st.apiKeyPrefix,
		st.passphraseCipher, st.hasPassphrase, st.defaultMode, st.defaultPath, st.defaultTargets, now); err != nil {
		return nil, apperror.Wrap(apperror.CodeInternalError, "save safekeep settings", err)
	}

	return s.GetSettings(ctx)
}

// TestConnection tests connectivity against SafeKeep using override inputs or saved settings.
func (s *Service) TestConnection(ctx context.Context, req TestRequest) (*TestResult, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = st.baseURL
	}
	apiKey := strings.TrimSpace(req.APIKey)
	usingSaved := false
	if apiKey == "" && st.apiKeyCipher != "" && s.sealer != nil {
		unsealed, uErr := s.sealer.UnsealToken(st.apiKeyCipher)
		if uErr != nil {
			return nil, apperror.Wrap(apperror.CodeInvalidArgument, "cannot decrypt stored safekeep api_key; re-enter api_key", uErr)
		}
		apiKey = unsealed
		usingSaved = true
	}

	upstreams, latencyMS, testErr := s.client.ListUpstreams(ctx, baseURL, apiKey)
	testedAt := time.Now().UTC()
	testStatus := "ok"
	testErrMsg := ""
	if testErr != nil {
		testStatus = "failed"
		testErrMsg = testErr.Error()
	}

	if s.pool != nil && (usingSaved || (req.BaseURL == "" && req.APIKey == "") || baseURL == st.baseURL) {
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO safekeep_settings (id, last_tested_at, last_test_status, last_test_error, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (id) DO UPDATE SET
				last_tested_at = EXCLUDED.last_tested_at,
				last_test_status = EXCLUDED.last_test_status,
				last_test_error = EXCLUDED.last_test_error
		`, settingsRowID, testedAt, testStatus, testErrMsg)
	}

	if testErr != nil {
		return &TestResult{
			OK:        false,
			LatencyMS: latencyMS,
			Upstreams: []UpstreamInfo{},
			Message:   testErr.Error(),
		}, testErr
	}

	return &TestResult{
		OK:        true,
		LatencyMS: latencyMS,
		Upstreams: upstreams,
		Message:   fmt.Sprintf("连接成功，可用上游存储 %d 个（延迟 %d ms）", len(upstreams), latencyMS),
	}, nil
}

// BackupPassphrase returns the unsealed optional migration archive passphrase if configured.
func (s *Service) BackupPassphrase(ctx context.Context) (string, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return "", err
	}
	if !st.hasPassphrase || st.passphraseCipher == "" || s.sealer == nil {
		return "", nil
	}
	pass, err := s.sealer.UnsealToken(st.passphraseCipher)
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInternalError, "unseal safekeep backup passphrase", err)
	}
	return pass, nil
}

// RevealSecret decrypts one saved credential only after an explicit admin request.
func (s *Service) RevealSecret(ctx context.Context, field string) (string, error) {
	if field != "api_key" && field != "backup_passphrase" {
		return "", apperror.New(apperror.CodeInvalidArgument, "invalid secret field")
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return "", err
	}
	ciphertext := st.apiKeyCipher
	if field == "backup_passphrase" {
		ciphertext = st.passphraseCipher
	}
	if ciphertext == "" {
		return "", apperror.New(apperror.CodeInvalidArgument, "该敏感信息尚未配置")
	}
	if s.sealer == nil {
		return "", apperror.New(apperror.CodeInternalError, "credential sealer not initialized")
	}
	value, err := s.sealer.UnsealToken(ciphertext)
	if err != nil {
		return "", apperror.New(apperror.CodeInvalidArgument, "无法解密已保存的敏感信息，请重新配置")
	}
	return value, nil
}

// UploadArchive uploads a migration archive byte slice using the persisted SafeKeep settings.
func (s *Service) UploadArchive(ctx context.Context, filename string, data []byte, opts UploadOptions) (*UploadResult, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(st.baseURL) == "" || st.apiKeyCipher == "" || s.sealer == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "SafeKeep URL 或 API Key 未配置，请先在「系统设置」中完善配置")
	}
	apiKey, err := s.sealer.UnsealToken(st.apiKeyCipher)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "无法解密已保存的 SafeKeep API Key，请重新配置", err)
	}

	if strings.TrimSpace(opts.Mode) == "" {
		opts.Mode = st.defaultMode
	}
	if strings.TrimSpace(opts.Path) == "" {
		opts.Path = st.defaultPath
	}
	if strings.TrimSpace(opts.Targets) == "" {
		opts.Targets = st.defaultTargets
	}

	return s.client.UploadBackup(ctx, st.baseURL, apiKey, filename, data, opts)
}

func normalizeUploadMode(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "async") {
		return "async"
	}
	return "sync"
}

func maskKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if len(raw) <= 6 {
		return "****"
	}
	if len(raw) <= 12 {
		return raw[:4] + "****"
	}
	return raw[:8] + "****" + raw[len(raw)-4:]
}
