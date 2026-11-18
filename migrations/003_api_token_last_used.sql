-- last_used_at records the most recent successful authentication of an API token, written at most once per minute.
ALTER TABLE api_tokens
    ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;
