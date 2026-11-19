# Agent 自主注册项目交付记录

## 任务背景与范围

为已有“全局记忆权限 + 各项目权限”增加平行的成员级创建项目权限，使获授权的 Agent 可以处理未注册仓库，完成项目注册后写入项目记忆。客户端“配置客户端”第二步同步提供中英文自主注册说明。

本次沿用现有 Go、PostgreSQL、MCP 与内嵌管理界面，不新增依赖、不修改 Token 格式。用户要求实现后使用独立 Agent 评审，修复后提交。

## 编排分工

- 主 Agent：后端权限、数据库迁移、MCP 注册工具、核心测试、README/DESIGN、集成验证与提交。
- 管理界面 Agent：Admin API、成员权限开关、中英文指引、快照兼容与后台回归测试。
- 独立评审 Agent：只读审查权限、并发、幂等、身份冲突和兼容性；复现并报告问题。
- 文档 Agent：根据已落地代码及主 Agent 提供的验证结果整理本文。

## 最终行为

### 授权模型

`tenant_members.can_create_project` 为独立布尔开关，默认 `false`。即使成员没有全局记忆权限，也可以单独授予创建项目权限；该权限不授予任何已有项目的访问权。

API Token 仍绑定租户和成员。鉴权读取成员当前授权，因此开启或撤销创建权限无需重新签发 Token。注册事务再次检查有效租户、成员、创建开关并锁定授权，避免授权撤销与提交之间的竞态。

Admin 快速接入、创建成员、绑定成员及修改成员权限支持此开关；列表和详情显示当前值。旧客户端更新时省略该字段会保留已有授权，创建或绑定时省略则默认关闭。系统快照导入导出保留该字段，旧快照缺失字段时默认关闭。

### 自主注册流程

1. Agent 使用仓库 Git remotes 或 `.engram-project` UUID 调用 `memory_discover`。
2. 未注册时返回 `PROJECT_NOT_REGISTERED`，错误的 `details.can_create_project` 表明当前成员是否可创建。
3. 有权限时调用 `project_register`，提供 `name`、`idempotency_key`，以及 `remotes` 或已有 `project_marker`；可传相对 Git 根目录的 `workspace_path`。
4. 注册事务创建项目、规范化仓库映射及当前成员的 `writer` 授权，返回项目身份。
5. Agent 重新调用 `memory_discover` 获取 `project_context`，然后读写项目记忆。

名称最多 200 字符；幂等键必填、最多 128 字节；remotes 最多 32 个，规范化、去重和排序后参与幂等计算。未提供 Marker 时服务端生成 UUID，但不自动写入本地文件。调用方不能通过参数指定租户、成员或授权角色。

### 冲突与幂等

已有项目名称、Marker 或相同/祖先/后代仓库路径会返回 `PROJECT_EXISTS`，包括停用项目；不会为已有项目补授访问权限。同仓库互不重叠的子目录仍可分别注册。

同一幂等键、相同参数返回原注册结果；同键不同参数返回 `IDEMPOTENCY_CONFLICT`。重放检查当前创建权限、新项目有效状态及当前 `writer` 授权，不能绕过权限撤销。

租户注册锁通过 `project.LockRegistration` 共享，用于串行化 MCP、后台项目创建、快速接入、仓库映射和知识包增量导入的身份写入。多租户导入按目标 UUID 排序取锁。

### 客户端说明

“配置客户端”第二步的可复制记忆使用规范新增：发现 `PROJECT_NOT_REGISTERED` 时，获授权成员可根据当前仓库自主调用 `project_register`，无需再次询问用户；成功后重新发现项目，无权限则联系管理员，已有项目无访问权时不得通过注册自行加权。中英文同步，README 与 DESIGN 同步说明工具和权限模型。

## 文件与数据变更

| 范围 | 主要文件 |
| --- | --- |
| 数据迁移 | `migrations/006_project_creation_permission.sql`、`migrations/project_creation_test.go` |
| 实时鉴权与权限输出 | `internal/auth/auth.go`、`internal/memory/models.go`、`internal/memory/service.go` |
| 注册事务与共享锁 | `internal/memory/project_registration.go`、`internal/project/registration.go` |
| MCP 入口和 Schema | `internal/mcp/server.go`、`internal/mcp/project_registration.go` |
| Admin 与快照 | `internal/admin/handler.go`、`internal/admin/migration.go` |
| 界面和中英文说明 | `internal/admin/ui/index.html`、`app.js`、`i18n.js` |
| 回归测试 | 新增注册测试、MCP 参数测试及后台权限/快照测试 |
| 设计与使用说明 | `README.md`、`DESIGN.md` |

## 测试与验证

以下为全部修复完成后的最终验证结果。

| 验证 | 当前结果 |
| --- | --- |
| `go test -race ./...` | 通过（`-count=1`），实际执行数据库集成测试 |
| `go vet ./...` | 通过 |
| `go build` | 通过 |
| `node --check internal/admin/ui/app.js` | 通过 |
| `node --check internal/admin/ui/i18n.js` | 通过 |
| `git diff --check` | 通过 |
| 浏览器检查 | 通过：默认关闭、全局 `none` 时独立开启、列表显示、撤销保存，以及三个客户端的中英文第二步自主注册说明 |

数据库验证使用隔离 PostgreSQL 18 实例 `127.0.0.1:55439`，分别设置 `ENGRAM_TEST_DSN`、`ENGRAM_ADMIN_TEST_DSN`、`ENGRAM_MIGRATION_TEST_DSN`，使用独立测试库，未使用生产数据库。

新增核心用例覆盖注册后发现及写入、权限独立性、跨租户隔离、重复身份无加权、并发注册、路径重叠冲突、无效输入、幂等冲突和重放撤权；迁移用例验证已有成员默认不获得创建权限。

## 独立评审与处理

| 级别 | 问题 | 处理与验证状态 |
| --- | --- | --- |
| P2 | 后台项目创建、快速接入、仓库映射和知识包增量导入未与 MCP 共用注册锁，并发检查后可能绕过身份查重。评审 Agent 已用隔离 schema 复现。 | 四个入口均已接入共享锁，并在成员或审计身份写入前持锁；多租户导入排序取锁。数据库锁等待及释放回归、独立复审均通过。 |

独立评审发现的 P2 已修复并完成复审，未发现新的明确阻断问题。

## 部署与回滚

- 发布包含迁移 006 的新服务端；启动时启用 `AutoMigrate` 会执行新增列迁移。未启用自动迁移的环境须先应用迁移，再运行新代码。
- 存量成员默认关闭创建权限，管理员按需开启；已有全局和项目授权保持不变。
- 不新增环境变量、框架或依赖，不要求重新签发客户端 API Token。
- 客户端重新获取工具列表后可使用 `project_register`；已复制旧记忆说明的客户端需更新为第二步的新说明。
- 回滚须使用与服务版本匹配的 schema/数据库备份；本次不提供自动 down migration。回滚前应备份数据库及快照。

## 风险与交付状态

创建能力仅作用于当前租户的新项目。已有项目访问仍由项目成员授权决定，后台管理员保留现有管理能力。

实现、评审问题修复、独立复审和最终全量验证均已完成。浏览器任务和临时服务已清理；仅提交本任务变更，保留用户原有未跟踪文件。
