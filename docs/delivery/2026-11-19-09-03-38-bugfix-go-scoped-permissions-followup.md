# 权限改造反馈修复

## 修复结果

本次处理用户追加的四类反馈，保持原先确认的权限矩阵和旧成员 admin 降权迁移规则。

### 1. 停用租户知识包导入

原问题是上一轮将审计身份解析与活跃租户校验绑定：知识包合法地创建或映射到 disabled 租户后，获取审计外键时被 `active tenant required` 拦截。

- 新增 `ensureConsoleAuditActorTx`，只确保 `system_webui/admin` 审计主体存在，不授予任何成员权限、不修改租户状态。
- `importKnowledgeBundlePayload` 在租户映射验证完成后使用该 helper，允许导入停用租户。
- `ensureAdminActorForTenantTx` 保留活跃租户校验，交互管理及 Service 的活跃租户约束不变。
- 新增 `internal/admin/migration_disabled_test.go`，修复前四条导入路径全部复现失败，修复后通过：新建 disabled 租户、按名称匹配现有 disabled 租户、single、tenant_map。
- 测试同时检查记忆、历史版本及事件完整导入，审计身份正确且不获得成员授权，停用状态保留，交互管理仍拒绝停用租户。

### 2. 迁移运维影响

`migrations/005_scoped_member_permissions.sql` 保持原样。用户已明确选择旧成员 admin 降为无权限，升级后须由后台重新授权；无需再次确认该设计。

README 补充说明：迁移会清除全部幂等缓存，跨升级窗口重试请求可能再次执行，`memory_create` 有重复创建风险。升级前应暂停写入并排空在途请求；重新授权后，先核对结果不确定的请求，再决定是否重试。数据库备份与服务版本需配套，无自动 down 迁移。

### 3. 国际化与权限界面测试

- 删除 11 个已无调用的租户管理员/维护者翻译键。
- 将“未生成”“未关联仓库，通过标记文件识别”翻译键改为 app 实际调用的纯文本，修复英文模式回退中文。
- 新增两处占位词条的翻译回归。
- 新增 `internal/admin/uitest/permissions.test.cjs`，在最小 DOM 替身中执行真实 `app.js` 函数，覆盖成员详情的三档全局权限 × 三档项目权限、项目授权选择器、提交 none 撤销项目和全局权限。未引入测试依赖。

### 4. SSE 与无权限游标

- SSE 复用原请求的 `AuthenticateRequest` 重验凭证与成员授权，不扩展 Principal 或数据库模型。仅吊销 API Token 或 Token 到期，也会在下次通知或 15 秒保活检查时断开现有连接。
- 独立复审进一步发现 Token 改绑身份时的订阅归属边界：重验后的租户与成员 ID 必须与连接创建时相同，否则立即断流；防止迁移还原或身份重建后继续接收旧范围通知。
- 无任何可读范围的 Discover 仍返回有效签名游标，但 revision 固定为 0，不暴露租户事件高水位；有可读范围的行为不变，Query/Changes 仍检查实时权限。
- 新增实际 HTTP + PostgreSQL + 通知 Hub 测试，覆盖项目撤权、全局撤权、无新通知时由 15 秒保活断流、保留成员授权时单 Token 吊销及 Token 过期断流，以及同一 Token 改绑其他租户或成员后的断流。
- SSE 测试先收取授权期间通知并通过游标屏障排空准备阶段通知，避免订阅初始化与撤权之间的测试竞态。

## 验证

使用独立 PostgreSQL 容器，admin、memory、migration、stream 使用不同测试数据库，未操作现有运行服务或数据库。

- 修复前停用租户四条导入路径均失败，修复后通过。
- 英文占位词条测试修复前失败，修复后通过。
- `go test -race ./...`：通过，包含 PostgreSQL 集成及实际 SSE 长连接测试。
- `go vet ./...`：通过。
- `node --test internal/admin/uitest/*.test.cjs`：14 项通过。
- JS 语法检查、`git diff --check`：通过。

独立复审发现的 1 个 P2（SSE 重验后的原身份绑定）已修复并复核。7 个 SSE 场景通过；两个 Token 改绑用例在旧实现上失败，在修复实现上通过，竞态模式重复 5 次通过。当前无未解决明确阻断项。详见 `docs/review/2026-11-19-review-go-scoped-permissions-followup.md`。首次评审报告已追加遗漏与后续复核说明，保留原始审查记录。

本轮无新依赖、配置项或数据库迁移；代码未部署。

## ChangeLog

### [Unreleased] - 2026-11-19

- Fixed：知识包包含或映射停用租户时，审计身份解析不再导致导入失败。
- Fixed：英文模式下项目标记及无仓库占位文本的翻译回退。
- Fixed：既有 SSE 连接在 API Token 吊销或到期后重新认证并断流。
- Fixed：无可读范围的 Discover 游标不再暴露租户事件高水位。
- Added：停用租户导入、前端三档授权与撤销、持续 SSE 撤权及凭证失效测试。
