ALTER TABLE admin_accounts
    ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    ADD COLUMN role TEXT NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'viewer')),
    ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN last_login_at TIMESTAMPTZ;

CREATE TABLE admin_sessions (
    id UUID PRIMARY KEY,
    username TEXT NOT NULL REFERENCES admin_accounts(username) ON DELETE CASCADE,
    password_version BIGINT NOT NULL,
    client_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX admin_sessions_username_idx ON admin_sessions(username, expires_at);
