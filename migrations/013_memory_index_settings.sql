-- Persistent system settings for MCP memory_discover scope indices (single-row persisted settings).
CREATE TABLE IF NOT EXISTS memory_index_settings (
    id TEXT PRIMARY KEY DEFAULT 'default',
    discover_pinned_limit INTEGER NOT NULL DEFAULT 10,
    discover_global_recent_limit INTEGER NOT NULL DEFAULT 5,
    discover_project_recent_limit INTEGER NOT NULL DEFAULT 50,
    index_summary_max_runes INTEGER NOT NULL DEFAULT 100,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (discover_pinned_limit BETWEEN 1 AND 100),
    CHECK (discover_global_recent_limit BETWEEN 1 AND 200),
    CHECK (discover_project_recent_limit BETWEEN 1 AND 500),
    CHECK (index_summary_max_runes BETWEEN 0 AND 500)
);
