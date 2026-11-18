# AI Shared Memory Server

Centralized MCP Memory Server providing shared Global Memory (tenant-wide) and Project Memory for AI coding agents (Codex, Claude Code, etc.), backed by PostgreSQL as the single source of truth with an embedded Admin Web UI and deterministic one-way Markdown export.

See [DESIGN.md](./DESIGN.md) for the complete technical design specification.

## Features

- **Embedded Admin Web UI (`/`)**: Complete initialization and management console compiled into the single `memory-server` binary (no separate CLI required):
  - First-boot temporary admin password printed to console output; forced password change on initial login.
  - Password reset flow triggered via `RESET_ADMIN=true` (or `reset_admin=true`) environment variable.
  - 1-click Quick Bootstrap wizard for Tenant, Principal, Project, Git Remote, `.ai-memory-project` Marker, and API Token with ready-to-copy MCP configuration.
  - Visual management of Tenants, Principals, Projects, Monorepo `workspace_path` mappings, API Tokens, soft-deleted Memory restoration, revision history inspection, and Markdown export.
- **Two-Tier Memory Hierarchy**: Tenant Global Memory (`scope = "global"`) and Project Memory (`scope = "project"`).
- **Streamable HTTP MCP Transport (`/mcp`)**: Exposes 8 standard MCP tools (`memory_discover`, `memory_query`, `memory_read`, `memory_create`, `memory_update`, `memory_delete`, `memory_changes`, `memory_promote`).
- **Transactional Concurrency & Idempotency**: Optimistic locking (`expected_version`), full revision snapshots (`memory_revisions`), tenant-serialized commit-ordered change feed (`memory_events`), and `idempotency_requests`.
- **Hybrid Search**: English FTS (`tsvector`), `pg_trgm` trigram similarity, and short Chinese keyword (`< 3` chars) `ILIKE` fallback.

## Quick Start

```bash
# 1. Build server binary
make build

# 2. Run unit and integration tests
make test

# 3. Start server (prints initial admin password on first startup)
./bin/memory-server --config config.example.toml

# 4. Open http://localhost:8080 in browser, sign in with the printed temporary password,
#    set a new admin password, and use the Web UI to initialize tenants, projects, and API tokens.

# 5. Reset admin password if forgotten
RESET_ADMIN=true ./bin/memory-server --config config.example.toml
```
