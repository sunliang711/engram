# 按范围授权：追加修复独立复审

日期：2026-11-19

## 复审结论

本轮确认并修复了停用租户知识导入的回归；独立复审又发现 SSE 凭证重验遗漏原始身份绑定的问题，主 Agent 已修复，新增回归测试已复核通过。当前未发现未解决的明确阻断问题。

上一轮审查没有覆盖停用租户的导入路径，因而遗漏了用户指出的回归；本报告补充该路径的实际验证，并取代上一轮报告中关于剩余测试范围的结论。

## 范围与方法

对照 `/tmp/engram-review-fix-baseline.X70YvB` 中的修复前文件，审查本轮增量：

- `internal/admin/migration.go` 和新增 `migration_disabled_test.go`。
- `internal/mcp/server.go` 和新增 `stream_permissions_test.go`。
- `internal/memory/service.go` 及 `permissions_test.go` 的零游标用例。
- `internal/admin/ui/i18n.js`、`uitest/i18n.test.cjs`、新增 `uitest/permissions.test.cjs`。
- README 中关于迁移、幂等缓存、SSE 和停用租户导入的说明。

遵循 `code-review-go` 及 Go 后端、安全、API 规则。除经主 Agent 明确授权补充 SSE 回归用例外，未修改实现代码，也未操作用户原有 `docker-compose 2.yaml`。

## 问题与修复复核

| 等级 | 问题与触发条件 | 修复及复核结果 |
| --- | --- | --- |
| P2，已修复 | 知识导入复用要求租户处于 active 的审计身份 helper；创建或映射 disabled 租户时导致整个事务失败。上一轮独立审查遗漏此路径。 | 导入调用纯审计 helper `ensureConsoleAuditActorTx`；交互调用仍经 `ensureAdminActorForTenantTx` 验证 active，Service 的 `VerifyAdminActor` 活跃限制也保留。四种导入路径实测通过，状态、数据及审计身份均保持正确，没有新增成员授权。 |
| P2，独立复审发现，已修复 | SSE 改用 `AuthenticateRequest` 重验凭证后，没有检查重验身份是否与建立连接时一致。若同一 Token 在系统数据还原等过程中改绑到其他租户或成员，而新身份仍有全局读取权限，global-only 连接会继续通过检查并沿用原租户订阅，即使原身份已降为 none。 | `internal/mcp/server.go:297` 同时比较重验结果与原身份的 TenantID 和 PrincipalID。独立新增同 Token 跨租户改绑、同租户更换成员两个回归用例；新身份保持全局读取，原身份撤权。两个用例在临时源码副本的旧实现上均因未断流而失败；在修复后通过，并重复 5 次通过竞态检查。 |

## 其他检查结论

- 停用租户导入只放宽迁移入口的目标状态限制，不激活租户，也不放开普通成员写入或后台交互写入。
- SSE 使用原请求的统一认证入口，继续遵循既有 Bearer、配置静态 Token、OIDC 和 mTLS 认证规则；没有引入新的认证模型。数据库 Token 的吊销、过期和成员读权限撤销均会在通知发送前或 15 秒保活检查时断流。
- SSE 测试先接收实际通知确认订阅就绪，再停止并等待发布协程退出，通过哨兵通知排空初始化帧后执行撤权或改绑；无通知场景实际等待保活检查。请求超时会使测试失败，不能充当“正常关闭”的假阳性。
- 无任何可读范围的 Discover 返回可验证的 revision=0 游标，不查询租户高水位；有可读范围时游标行为不变，query 和 changes 仍拒绝无权限上下文。
- 前端权限测试执行 app.js 中真实函数，覆盖全局与项目权限的 3×3 组合、项目权限选择器、none 撤销提交；翻译测试覆盖实际调用的两个占位键。
- README 明确说明迁移 005 清空全部幂等缓存以及跨升级重试可能重复写入的风险，迁移 SQL 未变更。

## 独立验证

使用任务指定的隔离 PostgreSQL 容器 `engram-review-fixes-test`，未操作其他服务数据库。

- `ENGRAM_ADMIN_TEST_DSN=… go test -race -count=1 ./internal/admin -run 'TestKnowledgeImportDisabledTenants|TestNormalizeSnapshotPermissions'`：通过，包含 disabled 新建、按名匹配、single、tenant_map 四条路径。
- `ENGRAM_TEST_DSN=… go test -race -count=1 ./internal/memory -run TestScopedMemberPermissions`：通过，包含已有租户事件时无权限上下文的零游标验证。
- `ENGRAM_STREAM_TEST_DSN=… go test -race -count=1 ./internal/mcp -run TestStreamClosesWhenReadPermissionRevoked`：7 个场景通过，包括实际 15 秒保活断流及两个身份改绑场景。
- `ENGRAM_STREAM_TEST_DSN=… go test -race -count=5 ./internal/mcp -run 'TestStreamClosesWhenReadPermissionRevoked/token_rebound'`：两项回归重复验证通过。
- `node --test internal/admin/uitest/*.test.cjs`：14 项通过。
- `git diff --check`：通过。

静态 Token、OIDC 和 mTLS 的长连接没有分别增加端到端用例；已核对本次复用原统一认证入口，没有改变这些凭证的来源和优先级。当前没有因此确认的行为回归。
