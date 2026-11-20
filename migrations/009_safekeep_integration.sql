-- SafeKeep remote backup integration configuration (single-row persisted settings).
CREATE TABLE IF NOT EXISTS safekeep_settings (
    id TEXT PRIMARY KEY DEFAULT 'default',
    enabled BOOLEAN NOT NULL DEFAULT false,
    base_url TEXT NOT NULL DEFAULT '',
    api_key_ciphertext TEXT NOT NULL DEFAULT '',
    api_key_prefix TEXT NOT NULL DEFAULT '',
    backup_passphrase_ciphertext TEXT NOT NULL DEFAULT '',
    has_backup_passphrase BOOLEAN NOT NULL DEFAULT false,
    default_mode TEXT NOT NULL DEFAULT 'sync',
    default_path TEXT NOT NULL DEFAULT 'engram',
    default_targets TEXT NOT NULL DEFAULT '',
    last_tested_at TIMESTAMPTZ,
    last_test_status TEXT NOT NULL DEFAULT '',
    last_test_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (default_mode IN ('sync', 'async'))
);
