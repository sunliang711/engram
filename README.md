# Engram

Centralized AI Shared Memory Server providing shared Global Memory (tenant-wide) and Project Memory for AI coding agents (Codex, Claude Code, etc.), backed by PostgreSQL as the single source of truth with an embedded Admin Web UI and deterministic one-way Markdown export.

See [DESIGN.md](./DESIGN.md) for the complete technical design specification.

## Features

- **Embedded Admin Web UI (`/`)**: Complete initialization and management console compiled into the single `engram-server` binary (no separate CLI required):
  - First-boot temporary admin password printed to console output; forced password change on initial login.
  - Password reset flow triggered via `RESET_ADMIN=true` (or `reset_admin=true`) environment variable.
  - 1-click Quick Bootstrap wizard for Tenant, Principal, Project, Git Remote, `.engram-project` Marker, and API Token with ready-to-copy MCP configuration.
  - Visual management of Tenants, Principals, Projects, Monorepo `workspace_path` mappings, API Tokens, soft-deleted Memory restoration, revision history inspection, and Markdown export.
- **Two-Tier Memory Hierarchy**: Tenant Global Memory (`scope = "global"`) and Project Memory (`scope = "project"`).
- **Streamable HTTP MCP Transport (`/mcp`)**: Exposes 9 MCP tools (`project_register`, `memory_discover`, `memory_query`, `memory_read`, `memory_create`, `memory_update`, `memory_delete`, `memory_changes`, `memory_promote`).
- **Transactional Concurrency & Idempotency**: Optimistic locking (`expected_version`), full revision snapshots (`memory_revisions`), tenant-serialized commit-ordered change feed (`memory_events`), and `idempotency_requests`.
- **Hybrid Search**: English FTS (`tsvector`), `pg_trgm` trigram similarity, and short Chinese keyword (`< 3` chars) `ILIKE` fallback.

## Quick Start

```bash
# 1. Build server binary
make build

# 2. Run unit and integration tests
make test

# 3. Start server (prints initial admin password on first startup)
./bin/engram-server --config config.example.toml

# 4. Open http://localhost:8080 in browser, sign in with the printed temporary password,
#    set a new admin password, and use the Web UI to initialize tenants, projects, and API tokens.

# 5. Reset admin password if forgotten
RESET_ADMIN=true ./bin/engram-server --config config.example.toml
```

### Docker Compose

```bash
# Local development (generates an ephemeral random signing key if ENGRAM_CONTEXT_SIGN_KEY is unset)
docker compose up -d

# Production / persistent sessions (recommended)
ENGRAM_CONTEXT_SIGN_KEY="$(openssl rand -hex 32)" docker compose up -d
```

## Configuration

- Copy [config.example.toml](./config.example.toml) to `config.toml` and adjust settings as needed.
- **Signing Key (`server.context_sign_key` / `ENGRAM_CONTEXT_SIGN_KEY`)**: Set a persistent >=32-byte secret key in production, recommended via:
  ```bash
  openssl rand -hex 32
  ```
  Placeholder values containing `change-me`, `replace-with`, or `example` are rejected on startup. When omitted, an ephemeral random key is generated at startup (existing context tokens and admin sessions will be invalidated across restarts).

## MCP Client Configuration

Engram exposes a **Streamable HTTP** MCP endpoint at `/mcp` (not `stdio`). First issue an API Token (`aim_...`) in the Admin Web UI (`http://localhost:8080`).

To view configuration again, use **Overview → View client configuration** and select an existing client, or paste a saved key. This does not create tenants, members, or keys. **Create key and configure** is only for issuing a new key; restarting the server does not require repeating quickstart.

### Codex (`~/.codex/config.toml`)

**Option 1: `http_headers` (recommended — works in both Codex Desktop App and CLI)**

```toml
[mcp_servers.engram]
enabled = true
url = "http://localhost:8080/mcp"
http_headers = { Authorization = "Bearer <YOUR_API_TOKEN>" }
```

**Option 2: `bearer_token_env_var` (for terminal/CLI workflows)**

```toml
[mcp_servers.engram]
enabled = true
url = "http://localhost:8080/mcp"
bearer_token_env_var = "ENGRAM_API_TOKEN"
```

Then export the variable before launching `codex`:

```bash
export ENGRAM_API_TOKEN="<YOUR_API_TOKEN>"
```

> **Note:** `bearer_token_env_var` expects the **name of an environment variable** (such as `ENGRAM_API_TOKEN`), not the raw `aim_...` token string. On macOS, GUI apps launched from the Dock/Finder usually do not inherit `~/.zshrc` exports, so Option 1 (`http_headers`) is recommended for the Codex Desktop App.

### JSON Clients (Claude Code / Cursor / etc.)

```json
{
  "mcpServers": {
    "engram": {
      "type": "streamable-http",
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_API_TOKEN>"
      }
    }
  }
}
```

### Member permissions

The console `admin` account manages members, tokens, grants, restore, and permanent purge. API/MCP members have independent permissions for global memories and each project:

| Scope | No access | Read only | Read/write (including soft delete) |
| --- | --- | --- | --- |
| Global memories | `none` | `member` | `global_writer` |
| Each project | No membership (`role: none` revokes it) | `reader` | `writer` |

### Agent attribution

Pass the optional top-level `agent_name` when calling `memory_create`, `memory_update`, `memory_delete`, or `memory_promote`, for example `"agent_name": "Codex"`, `"Claude Code"`, or `"review-agent"`. Names are trimmed, limited to 128 Unicode characters, and cannot contain internal control characters. They are self-reported labels; authorization still uses the authenticated member.

Memories retain `created_agent` and `updated_agent` alongside `created_by` / `updated_by`. Each version snapshot records the agent responsible for that version in `updated_agent`. Creation and promotion set both agent fields; later writes preserve the creator. Omitted or blank names are stored as empty strings and shown as **Unknown agent**. A write without a name, including a console operation, clears the latest agent instead of inheriting a previous writer's name. Historical versions keep their own attribution.

Use the same name when retrying an idempotency key; changing it returns `IDEMPOTENCY_CONFLICT`. Existing unnamed requests retain their previous request hashes. Migration `007_memory_agent_names.sql` adds empty attribution to existing memories without changing member identities. System snapshots, knowledge bundles, Markdown exports, and ZIP manifests preserve the agent fields; older archives remain compatible. Refresh the client tool list and copy the updated Step 2 instructions to enable reporting.

### Project creation

Project creation is a separate tenant-member permission, `can_create_project`, disabled by default. Enable **Allow agents to create projects autonomously** in the member settings (or quickstart). Existing API tokens inherit the live member grant; no token reissue is required. Migration `006_project_creation_permission.sql` adds the permission without changing existing grants. System snapshots preserve it; older snapshots default to disabled.

When `memory_discover` returns `PROJECT_NOT_REGISTERED`, its error details include `can_create_project`. Authorized agents can call `project_register` with `name`, Git `remotes`, optional relative `workspace_path` / existing `project_marker`, and an `idempotency_key`. A remote or marker is required. A nonempty registration `workspace_path` requires remotes because it persists a repository mapping. For marker-only registration, omit it and report the marker's current directory as `marker_path` during discovery; marker identity follows its UUID, not a stored directory. Registration atomically grants the creator `writer` access to the new project; call `memory_discover` again before writing project memories. Existing identities (including disabled projects and overlapping repository paths) return `PROJECT_EXISTS` without granting access. Lock contention returns retryable `LOCK_TIMEOUT` (HTTP 503); retry with the same idempotency key after the lock is released. Registration retries recheck current creation and project permissions. The client configuration dialog's Step 2 includes these instructions in Chinese and English.

In quickstart, checking the project creation option enables permission; leaving it unchecked preserves an existing member's permission and defaults new members to disabled. Revoke permission in the member settings.

Existing JSON fields `tenant_role` and `project_role` remain compatible. `can_maintain_project` is retained as `false`; members cannot restore or purge memories. Default searches and change feeds include only authorized scopes.

Migration `005_scoped_member_permissions.sql` revokes all global and project grants of former member `admin` roles within their tenant. Reauthorize these members in the console after upgrading. Existing `maintainer` grants become `writer`. Importing old system snapshots applies the same conversion. Console audit identities cannot authenticate as API members. Apply the migration with the new server version; rolling back requires restoring a database backup with the matching server version.

Migration 005 also clears **all cached idempotency responses**. A write retried across the upgrade (including `memory_create`) may execute again, even with the same `idempotency_key`. Before upgrading, pause writers and drain pending requests. After upgrading and reauthorizing members, reconcile requests with uncertain outcomes against existing memories before retrying them. Back up the database before migration; there is no automatic down migration.

Existing SSE streams reauthenticate their original credentials and member grants before each notice and every 15-second keepalive, and require the verified tenant and member IDs to match the original subscription. Revoked or expired API tokens therefore terminate existing streams on the next check. A `memory_discover` context without any readable scope receives a signed cursor at revision zero instead of the tenant event high-water mark; query and changes requests remain unauthorized.

Knowledge-bundle imports may create or map to disabled tenants without activating them. Import audit records use the console-only identity, while interactive console memory operations still require an active tenant.
