# Go 独立代码评审：按范围授权

日期：2026-11-19

## 审查结果

未发现明确问题。当前变更符合已确认的权限边界，没有发现需要阻断交付的安全问题或行为回归。

## 审查范围与基线

由独立 Agent 审查本次工作区变更及新增文件，未修改实现代码。排除用户原有未跟踪文件 `docker-compose 2.yaml`。

- 鉴权与数据访问：`internal/auth/auth.go`、`internal/memory/service.go`。
- 通知与长连接：`internal/notify/listener.go`、`internal/mcp/server.go`。
- 后台管理与导入：`internal/admin/handler.go`、`internal/admin/migration.go`。
- 授权界面：`internal/admin/ui/app.js`、`index.html`、`i18n.js`。
- 升级脚本：`migrations/005_scoped_member_permissions.sql`。
- 对应测试，以及 README 与 DESIGN 的权限说明。

基线为用户确认的需求、`code-review-go` Skill，以及 Go 后端、安全和 API 规则。沿用仓库现有 HTTP、pgx 和错误处理方式，未要求无关技术栈调整。

## 重点检查结论

- 成员 `admin` 不再授予全局或项目权限，项目访问必须获得显式授权；`maintainer` 不再作为有效项目权限。
- 全局无权限的项目成员在发现索引、默认搜索和变更拉取中不会得到全局记忆内容；无项目权限时不能读写该项目。
- 项目 `writer` 支持创建、更新和软删除；恢复、永久清理等管理服务校验专用后台审计身份。
- 后台审计身份没有成员角色，成员认证、成员绑定及后台 Token 签发均排除 `system_webui` 身份；管理路由继续受后台会话验证保护。
- 幂等缓存重放重新检查当前范围读写权限与项目上下文，不能以旧成功响应绕过已撤销的授权。
- 通知 Hub 区分全局与项目授权；长连接在通知发送前和保活时重新检查成员及项目权限。
- 数据库升级按租户撤销旧 `admin` 的项目授权并将其全局权限降为 `none`；不影响该成员在其他非管理员租户的显式项目授权。普通旧 `maintainer` 转为 `writer`。
- 旧系统快照导入执行相同的角色降级，移除后台身份的成员授权，并吊销其历史 API Token。
- 成员详情与项目详情均支持“无权限 / 只读 / 读写”，撤销项目权限通过删除项目成员记录实现。

## 独立验证

使用独立数据库 `engram_review_memory`、`engram_review_admin`、`engram_review_migration`，未操作开发者测试库或浏览器验证库。

- `go test -race -count=1 ./internal/memory ./internal/admin ./migrations`：通过，包括实际 PostgreSQL 集成测试。
- `go test -race ./internal/auth ./internal/notify ./internal/mcp`：通过。
- `node --test internal/admin/uitest/*.test.cjs`：10 项通过。
- `git diff --check`：通过。

## 残余测试范围

现有测试覆盖通知 Hub 的范围过滤和无可读范围的 SSE 请求拒绝，但没有直接建立持续 SSE 连接后撤销授权、再断言连接关闭时机的集成测试。已静态核对通知发送前及保活时的实时鉴权分支，未发现明确缺陷。浏览器交互验证由主 Agent 单独执行。

升级会有意撤销旧成员管理员权限，需由后台重新授权；这是用户明确选择的迁移行为。

## 后续复核说明

用户随后指出知识包导入停用租户的回归，原“未发现明确问题”结论未覆盖该路径。本轮已修复，并新增四种停用租户导入路径、前端三档权限与撤销提交、持续 SSE 撤权/Token 吊销/过期及无权限零游标测试。最新复审结论见 `2026-11-19-review-go-scoped-permissions-followup.md`；以上正文保留首次审查记录。
