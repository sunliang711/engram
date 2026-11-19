# 记忆 Agent 归属记录交付说明

## 背景与范围

同一成员可能通过 Codex、Claude Code 或自定义 Agent 写入记忆。原有成员审计字段只能区分成员，无法识别实际使用的 Agent。本次增加可选 Agent 名称，记录创建者、最后修改者及每个历史版本的操作 Agent，同时保留现有成员身份与权限模型。

本次仅包含记忆 Agent 归属功能，不包含 Quickstart 交互流程调整。

## 实现结果

### 写入与历史归属

`memory_create`、`memory_update`、`memory_delete`、`memory_promote` 接受可选顶层参数 `agent_name`。名称去除首尾空白后最多 128 个 Unicode 字符，不允许内部控制字符；省略、空字符串或纯空白表示未知 Agent。

迁移 007 为 `memories` 增加 `created_agent`、`updated_agent`，均为非空文本、默认空字符串。已有记忆的 Agent 信息保持未知，不根据成员或历史内容推断。

| 操作 | `created_agent` | `updated_agent` |
| --- | --- | --- |
| 创建记忆 | 本次 Agent | 本次 Agent |
| 提升为新全局记忆 | 本次 Agent | 本次 Agent |
| 修改、软删除 | 保留原创建 Agent | 本次 Agent；未提供时清空 |
| 后台恢复、置顶、删除、批量软删除 | 保留原创建 Agent | 清空，避免误归属上次 Agent |

每次写入生成的版本快照保存相应 `updated_agent`，用于显示该版本实际报告的 Agent；历史版本缺失字段时显示未知，不使用记忆最新 Agent 回填。`created_by`、`updated_by` 及版本 `actor_id` 继续保存认证成员。

### 幂等与权限

非空、规范化后的 `agent_name` 参与请求哈希。同一幂等键重试应保持名称一致，改变名称会返回 `IDEMPOTENCY_CONFLICT`。空名称不加入原有哈希载荷，保持升级前未命名请求的哈希兼容。

Agent 名称是自报标签，不参与认证、租户选择或权限授予。自报 `admin` 等名称不会获得任何额外权限。

### 管理界面与客户端说明

后台记忆详情显示创建 Agent 和最后修改 Agent，历史版本同时显示认证成员与该版本 Agent。旧记录或空名称统一显示“未知 Agent”；中英文文案同步。名称经 HTML 转义，避免自报标签作为页面代码执行。

“配置客户端”第二步的可复制说明新增四个写工具的顶层 `agent_name` 用法、示例名称及幂等重试要求。README、DESIGN 与 MCP 工具 Schema 同步该契约。

### 导入导出兼容

系统快照、知识包、Markdown Front Matter 和 ZIP 清单保留 `created_agent`、`updated_agent`；版本快照随原历史内容保留归属。旧包缺失字段时按空字符串处理，不回填不存在的归属信息。

## 文件范围

| 范围 | 文件 |
| --- | --- |
| 数据迁移 | `migrations/007_memory_agent_names.sql`、`migrations/agent_names_test.go` |
| 归属校验与写入 | `internal/memory/agent.go`、`models.go`、`service.go`、`agent_names_test.go` |
| MCP 参数 | `internal/mcp/server.go` |
| 后台查询及导入导出 | `internal/admin/handler.go`、`migration.go`、`migration_agent_test.go`、`handler_ui_test.go` |
| 详情、历史与中英文说明 | `internal/admin/ui/app.js`、`i18n.js` |
| 界面回归 | `internal/admin/uitest/agent-attribution.test.cjs`、`i18n.test.cjs` |
| Markdown 与 ZIP 导出 | `internal/exporter/exporter.go`、`exporter_test.go` |
| 使用与设计文档 | `README.md`、`DESIGN.md` |

未新增框架、第三方依赖或环境配置项。

## 分工与独立评审

主 Agent 负责后端归属记录、数据迁移、集成验证和最终提交；相关实现分工完成后交由独立 Agent 评审，文档 Agent 根据最终代码与主 Agent 提供的验证结果整理本文。

独立评审未发现阻断问题，结论通过。评审建议增加升级前请求哈希的固定 golden 回归，已采纳：集成用例使用固定的旧格式请求哈希，验证空名称写入与重放保持兼容，避免测试仅复用当前实现而掩盖兼容性变化。

## 验证结果

以下验证均已由主 Agent 完成，本文未重复执行测试。

| 验证 | 结果 |
| --- | --- |
| `go test -race ./... -count=1` | 通过，实际执行 PostgreSQL 集成测试 |
| Agent 归属专项测试 | 多 Agent、历史版本、后台操作、权限边界、参数校验与幂等冲突通过 |
| 升级兼容 golden 回归 | 空名称旧请求哈希及重放兼容通过 |
| 快照、知识包与导出回归 | 新字段保留、旧包兼容及归属展示通过 |
| `go vet ./...` | 通过 |
| Go 构建 | 通过 |
| Node 26 界面回归与脚本检查 | 通过 |
| Ego 浏览器实测 | 详情显示创建 Codex、最后修改 Claude Code；历史 v1 显示 Owner / Codex，v2 显示 Owner / Claude Code |

数据库测试使用真实 PostgreSQL 18 的四个隔离数据库，分别配置核心测试、后台测试、迁移测试和专用快照恢复测试 DSN。快照恢复用例仅运行于明确指定的可丢弃数据库，避免覆盖其他测试数据。

## 部署与客户端更新

- 发布包含 `007_memory_agent_names.sql` 的新服务端。启用 `AutoMigrate` 时启动自动应用迁移；关闭自动迁移的环境须先应用迁移，再运行新代码。
- 存量记忆的两个 Agent 字段默认空字符串，历史快照不回填；现有成员身份和授权保持不变。
- 旧客户端可继续省略 `agent_name`。要记录名称，应刷新 MCP 工具列表，并采用“配置客户端”第二步的新说明。
- 无需重新签发 Token。幂等重试须保持规范化后的 Agent 名称一致；未提供名称的请求保持原哈希兼容。
- 回滚前备份数据库，使用与目标服务版本匹配的 schema/数据库备份；本次未提供自动 down migration。

## 风险与限制

Agent 名称由客户端自报，可能缺失、重复或不准确，只适合展示与操作追踪，不是可信身份签名。任何权限判断仍必须使用认证成员与当前授权。

未提供名称或后台操作会把最新修改 Agent 记录为未知，这是避免错误继承上一位 Agent 的设计行为。历史版本仅展示当时保存的信息，不补写未知归属。

功能、验证和独立评审已完成，提交由主 Agent 统一执行。
