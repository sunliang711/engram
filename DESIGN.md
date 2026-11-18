# Engram 技术设计

## 1. 文档信息

- 文档状态：V1 设计基线
- 更新日期：2026-11-16
- 目标读者：架构、开发、测试与运维人员

## 2. 建设目标

建设一个中央 Memory Server，为 Codex、Claude Code 等 AI 工具提供跨设备共享的两级知识库：

- Global Memory：当前用户或团队内跨项目复用的知识。
- Project Memory：只属于指定项目的知识。
- 服务端事务提交后，其他客户端立即可以读取最新版本。
- 在线客户端可以收到变更提示；离线客户端可以通过变更游标补齐。
- PostgreSQL 是唯一事实来源。
- Markdown 是导出、审查和备份格式，不参与双向同步。

V1 不包含：

- 向量数据库和 Embedding。
- 自动语义合并。
- 客户端上下文强制热更新。
- Markdown 修改后反向导入数据库。
- 跨组织公开知识共享。

## 3. 技术选型

| 组件 | 技术 | 作用 |
| --- | --- | --- |
| MCP Server | Go | 业务服务、MCP 协议实现与内嵌管理端 |
| Admin Console | Embedded Web UI (`go:embed`) | 租户/身份初始化、项目注册、Token 签发、知识恢复与导出管理（无需独立 CLI） |
| MCP Transport | Streamable HTTP | 多客户端远程访问 |
| API | MCP Tools + Admin REST API | 统一 Agent 读写接口与管理后台接口 |
| Knowledge Storage | PostgreSQL | Memory 唯一事实来源 |
| Knowledge Format | Markdown | 人类可读的内容和导出格式 |
| Search | PostgreSQL FTS + `pg_trgm` | 英文全文检索及中文模糊检索 |
| Notification | PostgreSQL `LISTEN/NOTIFY` | 在线实例和客户端变更提示 |
| Durable Change Feed | PostgreSQL Event Table | 离线补偿和可靠同步 |
| Backup | PostgreSQL Backup + Git Export | 数据恢复、版本审查和灾备 |
| Deployment | Nomad | 服务生命周期管理 |
| Network | WireGuard + TLS | 跨设备安全访问 |
| Authentication | OIDC、API Token、mTLS + Admin Session | 客户端身份认证与 Web UI 管理员认证 |

Go 服务可以沿用 Fx、zerolog 和 GORM。关键事务、乐观锁以及 `LISTEN/NOTIFY` 建议使用显式 SQL 或 `pgx`，避免 ORM 隐藏关键更新条件。

## 4. 总体架构

```text
Codex / Claude Code / Other Agents
        |
        | Streamable HTTP + Authentication
        v
+--------------------------------------+
| Memory MCP Server                    |
|                                      |
| AuthN/AuthZ       Project Resolver   |
| Memory Service    Search Service     |
| Change Feed       Export Service     |
+-------------------+------------------+
                    |
                    v
              PostgreSQL
        +-----------+-----------+
        |                       |
   Memory Data             Durable Events
        |                       |
        +-------- NOTIFY -------+
                    |
             Markdown Export
                    |
                    v
                   Git
```

`LISTEN/NOTIFY` 只负责低延迟唤醒。持久化的 `memory_events` 才是可靠的变更记录。

## 5. 项目自动识别

### 5.1 客户端收集项目身份

客户端在任务开始时执行：

```bash
git rev-parse --show-toplevel
git remote -v
```

调用 `memory_discover` 时传入：

```json
{
  "project_marker": "optional-project-uuid",
  "marker_path": "services/api",
  "remotes": [
    {
      "name": "origin",
      "url": "git@github.com:eagle/project-a.git"
    }
  ],
  "workspace_path": "services/api",
  "allow_global_only": false
}
```

`workspace_path` 是当前工作目录相对于 Git 根目录的路径；`marker_path` 是找到 `.engram-project` 的目录相对于 Git 根目录的路径（位于 Git 根目录时为 `""`），用于支持 Monorepo 中的多个逻辑项目并让服务端判断 Marker 归属层级。

项目识别规则：

1. 客户端从当前工作目录向 Git 根目录查找最近的 `.engram-project`，并记录其相对路径 `marker_path`。
2. 位于逻辑子项目目录的 Marker（`marker_path != ""`）优先于 Git 根目录 Marker（`marker_path == ""`）；当同时存在已注册的 `canonical_remote + workspace_path` 时，比较 `marker_path` 与匹配到的 `workspace_path` 的路径前缀深度，优先采用更具体（路径更深）的项目身份。
3. 没有子项目 Marker 时，服务端使用标准化 Remote 和 `workspace_path` 做最长前缀匹配。
4. Git 根目录 Marker（`marker_path == ""`）只作为整个仓库项目的回退身份，不能覆盖已注册的更具体 `workspace_path`。
5. 最后尝试已登记的 Remote 别名；当多个候选项目在同一具体度下冲突时返回 `PROJECT_AMBIGUOUS`。
6. 当未传任何项目身份（非 Git 目录）、显式设置 `allow_global_only: true`，或已匹配项目但当前有效 Tenant Member 无该项目访问权限时，可降级返回 `mode = "global_only"` 的 `project_context`（`project_id = null`），仅允许访问 Tenant Global Memory；若显式请求具体项目且不允许降级但未匹配到注册项目，则返回 `PROJECT_NOT_REGISTERED`。禁止根据目录名称或本地绝对路径猜测项目。

Monorepo 中每个需要独立 Memory 的逻辑子项目应注册自己的 `workspace_path`，并可在子项目根目录保存独立 Marker。本地绝对路径不上传服务端。

全局 Agent Instructions 可以作为兼容方案，但不能保证每个 Agent 都必然执行发现流程。支持启动 Hook 的客户端应优先使用设备级 Hook 自动收集项目身份，不需要在每个项目中配置 MCP 地址。

### 5.2 Remote 标准化

以下地址应归一为同一个仓库标识：

```text
git@github.com:eagle/project-a.git
https://github.com/eagle/project-a.git
ssh://git@github.com/eagle/project-a.git
```

标准结果：

```text
github.com/eagle/project-a
```

标准化规则包括：

- Host 转换为小写。
- 去除协议、用户信息、默认端口和末尾 `.git`。
- 清理重复斜杠和末尾斜杠。
- 仓库路径的大小写规则按代码托管平台处理，不能无条件转为小写。
- 仓库改名、迁移和多个 Remote 使用别名表处理，不覆盖历史身份。

### 5.3 项目注册

项目注册属于管理操作，不允许普通 Agent 根据任意 Remote 自动创建项目。所有初始化和管理动作（创建 Tenant、创建 Principal、分配角色、注册项目与 Remote 别名、配置 Monorepo `workspace_path`、生成 `.engram-project` Marker 以及签发 API Token）统一在 `memory-server` 内嵌的 **Admin Web UI** 中完成，无需额外部署 `memoryctl` 命令行工具。

没有 Remote 的项目（或 Monorepo 子项目）可在 Web UI 中一键生成并复制 `.engram-project` Marker UUID。该文件只保存项目 UUID，不保存服务地址或凭证，建议提交到仓库。

`memory_discover` 返回一个不透明的 `project_context`。建议使用服务端 HMAC 签名的无状态 Token，包含 Tenant、Project、Principal 和签发时间，默认有效期 24 小时。Token 过期或在上下文压缩后丢失时，客户端重新调用 `memory_discover`；该上下文只提供项目提示，不能代替认证或每次请求的最新权限校验。

## 6. 认证与权限

WireGuard 只解决网络访问安全，不能替代应用层身份认证和项目授权。

### 6.1 Web UI 管理员初始化与密码重置

- **首次启动临时密码**：服务启动时通过 PostgreSQL 事务咨询锁检查 `admin_accounts` 表。若管理员账号 `admin` 尚未初始化，则自动生成高强度随机临时密码，保存 `bcrypt` 哈希并将 `must_change_password` 置为 `true`，同时在控制台输出一次性的初始临时密码。
- **环境变量 `RESET_ADMIN` 重置**：当环境变量 `RESET_ADMIN=true`（或 `1`）设置时，服务启动会在事务锁保护下生成新的随机临时密码打印到控制台，将 `must_change_password` 置为 `true`，并递增 `password_version` 使所有旧的管理员会话立即失效。
- **Web UI 强制改密**：管理员使用临时密码登录 Web UI 后，系统强制进入“重置管理员密码”页面，完成新密码设置前禁止访问其他管理接口；新密码生效后临时密码立即作废。若此时环境变量 `RESET_ADMIN` 仍处于开启状态，Web UI 顶部常驻展示告警横幅，提示管理员从部署配置中移除该环境变量。

### 6.2 客户端认证与授权规则

- 设备使用 OIDC Token、API Token 或 mTLS。
- 服务端根据认证信息确定 `tenant_id`、`principal_id` 和角色。
- Global Memory 定义为 Tenant Global，而不是整个服务器全局。
- Agent 默认只能写入 Project Memory。
- Global Memory 的创建和提升需要 `global_writer` 权限。
- 每次读取、查询、更新和获取变更都重新执行授权检查。
- 调用方不能通过参数指定 `created_by` 或 `updated_by`。

基础角色：

| 角色 | 权限 |
| --- | --- |
| `member` | 读取 Tenant Global Memory，不默认获得项目权限 |
| `reader` | 查询和读取显式授权项目的 Memory |
| `writer` | 创建、更新显式授权项目的 Memory |
| `maintainer` | 删除、恢复和管理项目知识 |
| `global_writer` | 创建或提升 Global Memory |
| `admin` | Tenant、项目和权限管理 |

## 7. MCP Tool 设计

V1 提供 8 个 Tool。

Tool 名只使用 ASCII 字母、数字和下划线，并控制在 64 个字符以内，避免 MCP 客户端或底层模型 Tool Calling 网关产生兼容性问题。

### 7.1 `memory_discover`

识别当前项目，返回：

- `project_context`（包含 `mode`: `"project"` 或 `"global_only"`）。
- 项目基本信息（`global_only` 模式下为 `null`）。
- Global Memory 和 Project Memory 的重点索引（`global_only` 模式下仅返回 Global Memory 索引）。
- 当前变更游标 `latest_cursor`。
- 当前客户端权限。

`memory_discover` 只返回置顶知识、分类统计和有限数量的最近条目，不返回完整知识库。

### 7.2 `memory_query`

统一承担分页列举和搜索：

```json
{
  "project_context": "ctx_xxx",
  "scopes": ["global", "project"],
  "query": "PostgreSQL migration",
  "categories": ["architecture"],
  "status": ["active"],
  "cursor": null,
  "limit": 20
}
```

`memory_query` 的 `query` 为空时表示分页列举索引。响应默认只返回元数据和内容摘要。

### 7.3 `memory_read`

```json
{
  "project_context": "ctx_xxx",
  "memory_id": "mem_xxx",
  "version": null
}
```

默认读取最新版本，也可以读取指定历史版本。

### 7.4 `memory_create`

请求包含：

- `project_context`。
- `scope`、`category`、`title` 和 `content`。
- `tags`、`is_pinned` 和结构化来源引用 `source_refs`。
- `idempotency_key`。

服务端根据认证信息记录创建者，并执行权限、大小、重复内容和敏感信息检查。

### 7.5 `memory_update`

更新必须携带预期版本：

```json
{
  "project_context": "ctx_xxx",
  "memory_id": "mem_xxx",
  "expected_version": 5,
  "patch": {
    "category": "architecture",
    "title": "PostgreSQL migration rules",
    "content": "...",
    "tags": ["postgresql", "migration"],
    "status": "active",
    "is_pinned": true
  },
  "idempotency_key": "task-123-update-1"
}
```

`patch` 只允许修改白名单字段，禁止修改 `scope`、`project_id`、创建者和来源 Memory。版本冲突时返回当前版本号和 Memory ID，客户端需要重新读取、合并并重试。服务端不自动覆盖，也不自动进行语义合并。

### 7.6 `memory_delete`

使用软删除并要求 `expected_version`。删除操作必须生成 revision 和 event。V1 不向普通 Agent 暴露恢复 Tool；通过 Admin Web UI 恢复已软删除的知识，恢复同样执行乐观锁并生成 revision 和 event。

### 7.7 `memory_changes`

```json
{
  "project_context": "ctx_xxx",
  "cursor": "cursor_xxx",
  "limit": 100
}
```

返回当前项目和 Tenant Global 范围内的授权变更，以及新的不透明游标。客户端不能直接把数据库 revision 当作授权凭证。

### 7.8 `memory_promote`

将 Project Memory 提升为 Global Memory：

- 保留原 Project Memory。
- 创建新的 Global Memory。
- 记录 `source_memory_id`。
- 要求 `global_writer` 权限。
- 只在目标 Global 作用域内执行内容重复检测，原 Project Memory 不属于重复项。
- 支持 `expected_version` 和 `idempotency_key`。

`content_hash` 用于返回重复候选和辅助人工判断，不建立严格唯一约束；请求重放由 `idempotency_key` 处理。

所有 Tool 的结构化响应都包含当前授权范围的 `latest_cursor`。客户端发现它与自己的 `known_cursor` 不一致时，可以调用 `memory_changes` 刷新；正确性不依赖服务端主动推送。

## 8. 核心数据模型

主要数据表：

```text
admin_accounts
tenants
principals
tenant_members
api_tokens
projects
project_repositories
project_members
memories
memory_revisions
memory_events
idempotency_requests
```

### 8.1 管理员账号、Tenant、Principal 与成员关系

```sql
CREATE TABLE admin_accounts (
    username TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    must_change_password BOOLEAN NOT NULL DEFAULT true,
    password_version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tenants (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('active', 'disabled'))
);

CREATE TABLE principals (
    id UUID PRIMARY KEY,
    identity_provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    display_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (identity_provider, subject),
    CHECK (status IN ('active', 'disabled'))
);

CREATE TABLE tenant_members (
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    principal_id UUID NOT NULL REFERENCES principals(id),
    role TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, principal_id),
    CHECK (role IN ('member', 'global_writer', 'admin'))
);

CREATE TABLE api_tokens (
    id UUID PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    principal_id UUID NOT NULL REFERENCES principals(id),
    name TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

所有有效 Tenant Member 可以读取 Tenant Global Memory。只有 `global_writer` 和 `admin` 可以写入或提升 Global Memory。Tenant Member 默认不能访问所有 Project，除 `admin` 外必须在 `project_members` 中获得显式项目权限。

### 8.2 `projects` 与 `project_members`

```sql
CREATE TABLE projects (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    name TEXT NOT NULL,
    marker UUID,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, marker)
);

CREATE TABLE project_members (
    project_id UUID NOT NULL REFERENCES projects(id),
    principal_id UUID NOT NULL REFERENCES principals(id),
    role TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, principal_id),
    CHECK (role IN ('reader', 'writer', 'maintainer'))
);
```

### 8.3 `project_repositories`

```sql
CREATE TABLE project_repositories (
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
```

同一 Project 可以登记多条 Repository 记录作为 Remote 别名。匹配 Monorepo 时，在相同 `canonical_remote` 下选择能够覆盖客户端 `workspace_path` 的最长已注册路径。

### 8.4 `memories`

```sql
CREATE TABLE memories (
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
```

`source_refs` 中每一项使用受控结构保存来源类型、稳定标识和可选展示信息。服务端禁止客户端把本地凭证或带凭证的 URL 写入来源引用。

### 8.5 `memory_revisions`

Revision 保存完整快照，而不只是正文：

```sql
CREATE TABLE memory_revisions (
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
```

### 8.6 `memory_events`

```sql
CREATE TABLE memory_events (
    revision BIGSERIAL PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    project_id UUID REFERENCES projects(id),
    memory_id UUID NOT NULL REFERENCES memories(id),
    operation TEXT NOT NULL,
    memory_version BIGINT NOT NULL,
    actor_id UUID NOT NULL REFERENCES principals(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`BIGSERIAL` 在执行 `INSERT` 时分配序号，天然不代表事务提交顺序。V1 在分配事件序号前获取 Tenant 级事务咨询锁，并持有到提交，使同一 Tenant 内已提交事件的 `revision` 顺序与提交顺序一致：

```sql
SELECT pg_advisory_xact_lock(
    hashtextextended('memory-events:' || $tenant_id::text, 0)
);
```

主要业务写入应先完成，再获取锁、插入 Event、完成幂等结果并提交。锁内任一步骤失败都会回滚整个事务；事务回滚造成的 Sequence 空洞是允许的，因为对应事件从未可见。

`memory_events.revision` 只表示同一 Tenant 内的可靠变更顺序，不代表客户端对所有事件都有读取权限。`memory_changes` 必须按 Tenant、当前 Project 和 Tenant Global 范围过滤，并返回不透明游标。分页结果不足 `limit` 时，游标可以推进到当前 Tenant 的已提交高水位，避免反复扫描其他 Project 的无关事件。

建议增加以下索引：

```sql
CREATE INDEX idx_memory_events_tenant_revision
    ON memory_events (tenant_id, revision);

CREATE INDEX idx_memory_events_project_revision
    ON memory_events (tenant_id, project_id, revision);
```

### 8.7 `idempotency_requests`

```sql
CREATE TABLE idempotency_requests (
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
```

写事务在 PostgreSQL 默认的 `READ COMMITTED` 隔离级别下执行。首次请求在业务事务内通过 `INSERT ... ON CONFLICT DO NOTHING` 插入 `in_progress` 记录，并在同一事务提交前更新为 `completed`。并发请求命中相同唯一键时受行锁阻塞等待首个事务结束：首个事务提交后，等待方读取已提交的 `completed` 记录并返回已保存结果；首个事务因业务错误回滚后，插入的 `in_progress` 记录随之回滚，由等待方继续执行；等待超过 `lock_timeout` 时返回可重试的 `IDEMPOTENCY_IN_PROGRESS`。因此正常情况下不会存在已提交的 `in_progress` 记录。相同 Key 对应不同 `request_hash` 时返回 `IDEMPOTENCY_CONFLICT`。已完成结果默认保留 72 小时；如果发现异常提交的 `in_progress`，应记录告警并由修复任务处理，不能直接重放写操作。

## 9. 事务、并发与幂等

更新使用条件 SQL 实现乐观锁：

```sql
UPDATE memories
SET category = $1,
    title = $2,
    content_md = $3,
    tags = $4,
    source_refs = $5,
    is_pinned = $6,
    status = $7,
    content_hash = $8,
    version = version + 1,
    updated_by = $9,
    updated_at = now()
WHERE id = $10
  AND tenant_id = $11
  AND version = $12
  AND deleted_at IS NULL
RETURNING version;
```

实际实现根据 `patch` 字段白名单生成更新列，未出现在 `patch` 中的字段保持原值；无有效字段的空 Patch 返回 `INVALID_ARGUMENT`。

完整写事务：

```text
BEGIN
  校验认证、权限和 project_context
  创建或检查 idempotency_key
  校验 expected_version
  更新 memories
  插入 memory_revisions
  获取 Tenant 级事务咨询锁
  插入 memory_events
  保存包含 latest_cursor 的幂等结果
  执行 pg_notify(event revision)
COMMIT
```

PostgreSQL 会在事务提交后投递 `NOTIFY`。事件咨询锁会串行化同一 Tenant 的事件提交，但不会阻塞其他 Tenant，也不会串行化事件锁之前的业务处理。

条件更新没有返回行时使用以下判定矩阵，避免通过错误码探测资源：

1. Memory 不存在，或者调用方没有该 Memory 的读取权限：统一返回 `MEMORY_NOT_FOUND`。
2. 调用方有读取权限但没有相应写入权限：返回 `UNAUTHORIZED`。
3. 调用方有写入权限但版本不匹配：返回 `VERSION_CONFLICT`，只附带最新版本号，不返回完整正文。

`idempotency_key` 应至少在认证主体和 Tool 维度唯一，并保存请求摘要。相同 Key 对应不同请求内容时返回 `IDEMPOTENCY_CONFLICT`。

## 10. 变更通知与离线补偿

V1 的正确性只依赖持久化 Event Feed 和 `memory_changes`，不假设 MCP 客户端会处理自定义通知，也不假设通知能够自动触发 Agent 刷新上下文。

所有 Tool 的结构化响应返回：

```json
{
  "latest_cursor": "cursor_xxx"
}
```

客户端持有自己的 `known_cursor`。发现游标变化后，通过 `memory_changes` 获取授权范围内的事件并更新本地游标。断线客户端下一次调用任意 Tool 时也能发现变化。

对于明确声明支持接收通知的客户端，服务端可以通过活动中的 Streamable HTTP 发送最小变更提示：

```json
{
  "latest_cursor": "cursor_xxx"
}
```

主动通知属于协商后的客户端扩展，只用于降低延迟，不携带完整正文，也不进入 V1 验收所依赖的一致性链路。标准 MCP Resource Subscription 在确认目标客户端支持后再作为后续增强实现。

## 11. 搜索设计

V1 搜索顺序：

1. 标题精确匹配和前缀匹配。
2. Category 和 Tags 过滤。
3. PostgreSQL FTS 处理英文全文检索。
4. `pg_trgm` 处理长度足够的中文及模糊匹配。
5. 少于 3 个 Unicode 字符的中文关键词，在作用域过滤后使用 `ILIKE` 子串匹配兜底。
6. Global 与当前 Project 分别检索，再按相关性合并排序。

所有查询必须强制附加作用域条件：

```text
tenant_id = current tenant
AND (
  scope = global
  OR project_id = current project
)
```

索引建议：

```sql
CREATE INDEX idx_memories_project
    ON memories (tenant_id, project_id, status)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_memories_tags
    ON memories USING gin (tags)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_memories_fts
    ON memories USING gin (fts_doc)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_memories_title_trgm
    ON memories USING gin (title gin_trgm_ops)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_memories_content_trgm
    ON memories USING gin (content_md gin_trgm_ops)
    WHERE deleted_at IS NULL;
```

两字中文查询使用 Trigram 相似度运算时可能没有可用的共同 Trigram；`ILIKE` 仍能得到正确结果，但通常无法有效利用 Trigram 索引。V1 接受在 Tenant 和 Project 过滤后的有限数据集上执行该兜底扫描，并在上线前使用真实中文知识样本验证正确性和延迟。中文分词与向量检索保留为后续能力。

## 12. 知识治理与安全

每条知识至少保留：

- 来源任务、文件或 URL。
- 创建者和最后修改者。
- 完整修订历史。
- `active`、`deprecated` 和 `archived` 状态。
- 内容哈希和重复内容提示。
- 删除和恢复记录。

服务端限制：

- 单条标题、正文和标签大小。
- Markdown 中的危险链接和协议。
- 凭证、私钥及常见 Token 格式。
- Agent 自动提升 Global Memory。
- 单客户端写入频率和批量大小。
- Tool 参数中的未知字段和非法状态转换。

Memory 内容必须被客户端视为外部数据，不能获得高于系统指令和项目规则的优先级，以降低知识库提示注入风险。

## 13. Markdown 导出与备份

建议导出结构：

```text
export/
  tenants/{tenant_id}/global/{memory_id}.md
  tenants/{tenant_id}/projects/{project_id}/{memory_id}.md
```

文件包含稳定 Front Matter：

```yaml
---
id: mem_xxx
version: 6
scope: project
project_id: project_xxx
status: active
updated_at: 2026-11-16T10:00:00Z
---
```

导出与备份规则：

- 导出是单向、确定性的，同一数据重复导出结果一致。
- 客户端可控的 `category`、标题和标签不参与文件路径拼接，只写入 Front Matter。
- Git 不参与在线读写，不作为数据库事务的一部分。
- 导出任务按时间或事件批量执行。
- Memory 被软删除或作用域变化时，导出器根据稳定 ID 清理旧文件。
- PostgreSQL 执行定期全量备份和 WAL/PITR。
- 备份需要加密、校验、设置保留周期并定期执行恢复演练。
- Git 快照不能替代 PostgreSQL 备份，因为它不包含完整权限、事件和幂等状态。

## 14. 部署方案

Nomad Job 建议：

```text
memory-server     长期运行，可多副本
memory-exporter   Periodic Job
memory-backup     Periodic Job
postgresql        独立持久化服务或托管实例
```

网络链路：

```text
Client
  -> WireGuard
  -> TLS Reverse Proxy
  -> Memory Server
  -> PostgreSQL
```

多副本 Server 都监听 PostgreSQL 通知。请求不依赖本地内存状态，HMAC 签名的 `project_context` 可由所有实例验证；实例仍需根据当前认证主体查询最新成员关系，Token 本身不授予权限。

服务至少提供：

- Liveness 和 Readiness 检查。
- 数据库连接池指标。
- Tool 调用延迟和错误率。
- 版本冲突、幂等命中和搜索命中指标。
- Event 延迟和导出积压指标。
- 使用英文输出的结构化日志。

日志禁止记录认证凭证、完整 Memory 正文和未经清理的 Remote URL 凭证部分。

## 15. 标准错误码

```text
UNAUTHENTICATED
UNAUTHORIZED
PROJECT_NOT_REGISTERED
PROJECT_AMBIGUOUS
PROJECT_CONTEXT_INVALID
MEMORY_NOT_FOUND
VERSION_CONFLICT
DUPLICATE_MEMORY
IDEMPOTENCY_CONFLICT
IDEMPOTENCY_IN_PROGRESS
INVALID_ARGUMENT
RATE_LIMITED
INTERNAL_ERROR
```

所有错误响应包含 `request_id`。可重试错误需要明确返回 `retryable`，避免 Agent 对权限错误或参数错误进行无意义重试。

## 16. 标准数据流

```text
客户端识别仓库
    -> memory_discover(repository identity)
    -> 服务端认证、授权并解析 project_context
    -> 返回重点索引和 revision cursor
    -> Agent 按需 memory_query/memory_read
    -> Agent 携带 expected_version 和 idempotency_key 写入
    -> 同一事务写 memories、revisions 和 events
    -> Tenant 级事件锁保证 Event 提交顺序
    -> 事务提交并发送可选 NOTIFY
    -> Tool 响应返回 latest_cursor
    -> 客户端通过 memory_changes(cursor) 补齐
```

## 17. V1 实施阶段

### 阶段一：核心基础

- 数据库迁移。
- 认证和基础角色权限。
- 项目注册、Marker 和 Remote 标准化。
- `memory_discover`、`memory_query` 和 `memory_read`。

### 阶段二：可靠写入

- `memory_create`、`memory_update` 和 `memory_delete`。
- 乐观锁、完整 Revision 和软删除。
- 幂等请求和标准错误模型。

### 阶段三：共享与同步

- 持久化 Event Feed。
- `memory_changes`。
- `LISTEN/NOTIFY` 和可选客户端通知。
- `memory_promote` 和 Global Memory 权限。

### 阶段四：搜索与运维

- 英文 FTS、中文 Trigram 和短关键词子串搜索。
- Markdown 确定性导出。
- PostgreSQL 备份、PITR 和恢复演练。
- Nomad 部署、指标、日志和告警。

## 18. 验收标准

V1 至少满足以下验收场景：

1. 两个 Agent 同时基于同一版本更新，只有一个成功，另一个收到 `VERSION_CONFLICT`。
2. 并发事务即使以不同于启动顺序的次序提交，客户端也不会因为推进 Cursor 而漏掉较晚提交的事件。
3. Project A 的普通查询无法读取 Project B 的 Memory。
4. Tenant A 无法通过 Memory ID 或项目 Remote 探测 Tenant B 的数据。
5. 相同幂等请求重复提交不会产生重复 Memory 或重复版本。
6. SSH、HTTPS 和仓库别名能够识别为同一注册项目。
7. Monorepo 中的子项目 Marker 和最长 `workspace_path` 匹配不会被 Git 根目录 Marker 覆盖。
8. 一字、两字及更长中文关键词都能返回正确结果，并满足约定的项目级延迟指标。
9. Markdown 导出按 Tenant 隔离，客户端输入不能改变导出根目录或产生路径穿越。
10. Markdown 导出失败不会影响在线事务提交。
11. PostgreSQL 备份可以在隔离环境恢复，并通过数据一致性校验。
12. 删除、管理端恢复和提升操作具有完整审计记录。
13. 不保持常驻连接且错过通知的客户端，仍能通过 `latest_cursor` 和 `memory_changes` 补齐全部授权变更。

## 19. 后续演进

V1 稳定后再评估：

- 中文分词扩展。
- Embedding 和混合检索。
- Memory 自动归档和冲突建议。
- 人工审核工作流。
- 标准 MCP Resource 和订阅能力。
- 基于来源可信度和时效性的检索排序。
- 跨项目知识引用，而不是复制正文。
