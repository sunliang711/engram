-- Audit logs, audit inspection task runs, scheduled task definitions, and scheduled task run history.
-- Audit tables intentionally omit foreign keys to tenant/project/principal tables so audit records
-- outlive deleted entities, matching memory_events semantics.

CREATE TABLE IF NOT EXISTS audit_logs (
    id UUID PRIMARY KEY,
    tenant_id UUID,
    project_id UUID,
    actor_type TEXT NOT NULL DEFAULT '',
    actor_id TEXT NOT NULL DEFAULT '',
    actor_name TEXT NOT NULL DEFAULT '',
    agent_name TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'success',
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('success', 'failed', 'denied')),
    CHECK (jsonb_typeof(details) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at
    ON audit_logs (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_audit_logs_tenant_created_at
    ON audit_logs (tenant_id, created_at DESC)
    WHERE tenant_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_audit_logs_action_created_at
    ON audit_logs (action, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_logs_source_status_created_at
    ON audit_logs (source, status, created_at DESC);

CREATE TABLE IF NOT EXISTS audit_task_runs (
    id UUID PRIMARY KEY,
    task_type TEXT NOT NULL,
    trigger_type TEXT NOT NULL DEFAULT 'manual',
    status TEXT NOT NULL DEFAULT 'running',
    tenant_id UUID,
    actor_name TEXT NOT NULL DEFAULT '',
    findings_count INT NOT NULL DEFAULT 0,
    summary TEXT NOT NULL DEFAULT '',
    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    error_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    CHECK (status IN ('running', 'success', 'failed')),
    CHECK (jsonb_typeof(findings) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_audit_task_runs_started_at
    ON audit_task_runs (started_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS scheduled_tasks (
    name TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    task_type TEXT NOT NULL DEFAULT 'builtin',
    enabled BOOLEAN NOT NULL DEFAULT true,
    interval_seconds BIGINT NOT NULL DEFAULT 3600,
    config_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ,
    last_status TEXT NOT NULL DEFAULT 'idle',
    last_duration_ms BIGINT NOT NULL DEFAULT 0,
    last_summary TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    run_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (interval_seconds > 0),
    CHECK (last_status IN ('idle', 'running', 'success', 'failed')),
    CHECK (jsonb_typeof(config_json) = 'object')
);

CREATE TABLE IF NOT EXISTS scheduled_task_runs (
    id UUID PRIMARY KEY,
    task_name TEXT NOT NULL REFERENCES scheduled_tasks(name) ON DELETE CASCADE,
    trigger_type TEXT NOT NULL DEFAULT 'scheduled',
    status TEXT NOT NULL DEFAULT 'running',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    affected_rows BIGINT NOT NULL DEFAULT 0,
    summary TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    CHECK (trigger_type IN ('scheduled', 'manual')),
    CHECK (status IN ('running', 'success', 'failed')),
    CHECK (jsonb_typeof(details) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_scheduled_task_runs_task_started
    ON scheduled_task_runs (task_name, started_at DESC, id DESC);
