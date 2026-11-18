CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS admin_accounts (
    username TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    must_change_password BOOLEAN NOT NULL DEFAULT true,
    password_version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tenants (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('active', 'disabled'))
);

CREATE TABLE IF NOT EXISTS principals (
    id UUID PRIMARY KEY,
    identity_provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    display_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (identity_provider, subject),
    CHECK (status IN ('active', 'disabled'))
);

CREATE TABLE IF NOT EXISTS tenant_members (
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    principal_id UUID NOT NULL REFERENCES principals(id),
    role TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, principal_id),
    CHECK (role IN ('member', 'global_writer', 'admin'))
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id UUID PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL DEFAULT '',
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    principal_id UUID NOT NULL REFERENCES principals(id),
    name TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS projects (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    name TEXT NOT NULL,
    marker UUID,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, marker),
    CHECK (status IN ('active', 'disabled'))
);

CREATE TABLE IF NOT EXISTS project_members (
    project_id UUID NOT NULL REFERENCES projects(id),
    principal_id UUID NOT NULL REFERENCES principals(id),
    role TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, principal_id),
    CHECK (role IN ('reader', 'writer', 'maintainer'))
);

CREATE TABLE IF NOT EXISTS project_repositories (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    project_id UUID NOT NULL REFERENCES projects(id),
    canonical_remote TEXT NOT NULL,
    workspace_path TEXT NOT NULL DEFAULT '',
    is_primary BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, canonical_remote, workspace_path),
    CHECK (workspace_path !~ '(^/|(^|/)\.\.(/|$))')
);

CREATE TABLE IF NOT EXISTS memories (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    scope TEXT NOT NULL,
    project_id UUID REFERENCES projects(id),
    category TEXT NOT NULL,
    title TEXT NOT NULL,
    content_md TEXT NOT NULL,
    fts_doc TSVECTOR GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A')
        || setweight(to_tsvector('english', coalesce(content_md, '')), 'B')
    ) STORED,
    tags TEXT[] NOT NULL DEFAULT '{}',
    source_refs JSONB NOT NULL DEFAULT '[]',
    is_pinned BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'active',
    version BIGINT NOT NULL DEFAULT 1,
    source_memory_id UUID REFERENCES memories(id),
    content_hash TEXT NOT NULL,
    created_by UUID NOT NULL REFERENCES principals(id),
    updated_by UUID NOT NULL REFERENCES principals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CHECK (
        (scope = 'global' AND project_id IS NULL)
        OR
        (scope = 'project' AND project_id IS NOT NULL)
    ),
    CHECK (scope IN ('global', 'project')),
    CHECK (status IN ('active', 'deprecated', 'archived')),
    CHECK (category ~ '^[a-z0-9][a-z0-9_-]{0,31}$'),
    CHECK (jsonb_typeof(source_refs) = 'array')
);

CREATE TABLE IF NOT EXISTS memory_revisions (
    id BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    memory_id UUID NOT NULL REFERENCES memories(id),
    version BIGINT NOT NULL,
    operation TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    actor_id UUID NOT NULL REFERENCES principals(id),
    request_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (memory_id, version)
);

CREATE TABLE IF NOT EXISTS memory_events (
    revision BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    project_id UUID REFERENCES projects(id),
    memory_id UUID NOT NULL REFERENCES memories(id),
    operation TEXT NOT NULL,
    memory_version BIGINT NOT NULL,
    actor_id UUID NOT NULL REFERENCES principals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS idempotency_requests (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    principal_id UUID NOT NULL REFERENCES principals(id),
    tool_name TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    response_json JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, principal_id, tool_name, idempotency_key),
    CHECK (status IN ('in_progress', 'completed'))
);

CREATE INDEX IF NOT EXISTS idx_memory_events_tenant_revision
    ON memory_events (tenant_id, revision);

CREATE INDEX IF NOT EXISTS idx_memory_events_project_revision
    ON memory_events (tenant_id, project_id, revision);

CREATE INDEX IF NOT EXISTS idx_memories_project
    ON memories (tenant_id, project_id, status)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_memories_global
    ON memories (tenant_id, status)
    WHERE scope = 'global' AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_memories_content_hash
    ON memories (tenant_id, scope, project_id, content_hash)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_memories_tags
    ON memories USING gin (tags)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_memories_fts
    ON memories USING gin (fts_doc)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_memories_title_trgm
    ON memories USING gin (title gin_trgm_ops)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_memories_content_trgm
    ON memories USING gin (content_md gin_trgm_ops)
    WHERE deleted_at IS NULL;
