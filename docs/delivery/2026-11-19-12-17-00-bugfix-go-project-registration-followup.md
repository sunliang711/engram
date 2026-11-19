# 项目自主注册反馈修复

## 核实结果

1. 注册锁等待超时确实被包装为 `INTERNAL_ERROR`。原错误已经具有 `retryable: true`，但错误类别及 HTTP 映射不准确。
2. Marker-only 注册的目录参数确实没有持久化。现有 Marker 按 UUID 识别，目录来自发现请求；保留这个可移动身份模型，收紧注册参数并对齐文档。
3. 前端测试替身没有模拟 `checked`，导致未覆盖创建权限的实际序列化行为。
4. Quickstart 原本按表单覆盖，默认未勾选会撤销已有成员的创建权限，存在误操作风险。
5. 项目创建者审计属于可选功能扩展，本轮不新增字段或调整成员删除逻辑。

## 修复

- `LockRegistration` 将 PostgreSQL `55P03` 转为 `LOCK_TIMEOUT`，可重试，HTTP 状态映射为 503；MCP 沿用其工具错误传输格式。
- 锁超时后事务整体回滚，不留下项目或幂等记录，锁释放后可使用原幂等键重试。
- 非空注册 `workspace_path` 必须同时提供 remotes。Marker-only 注册省略目录，发现时通过 `marker_path` 报告标记文件当前目录。同步 MCP Schema、README、DESIGN 和客户端中英文说明。
- 前端 DOM 替身将 `checked` 初始化为 `false`；测试覆盖允许/不允许列、复选框回显与草稿、切换成员、停用状态、创建和绑定成员，以及提交显式 `true`/`false`。
- Quickstart 勾选时发送 `true`，未勾选时省略字段；后端保留已有成员权限，新成员默认关闭。中英文提示说明撤销应在成员设置完成，管理 API 显式 `false` 仍可撤销。

## 验证

- 隔离 PostgreSQL 18 实例下 `go test -race ./... -count=1` 通过，实际运行数据库集成测试。
- 真实 3 秒锁超时验证错误码、可重试标记、HTTP 映射、无部分写入，以及释放锁后的同键重试。
- Marker-only 注册拒绝目录参数，省略后可成功注册并在发现时报告当前标记目录。
- Quickstart 真实数据库回归验证已有 `true` 在省略时保留、显式 `false` 撤销，以及已有 `false` 在省略时保留。
- `node --test internal/admin/uitest/*.test.cjs` 21 项全部通过；独立评审 Agent 复跑同样通过。
- `go vet ./...`、JavaScript 语法检查与 `git diff --check` 通过。
- 独立 Agent 评审通过，未发现阻断问题。

## 影响与兼容

无数据库迁移、依赖升级或 Token 格式变化。原先仅 Marker 且指定非空注册目录的请求现在返回 `INVALID_ARGUMENT`，应省略注册目录；Marker UUID 发现方式保持一致。Quickstart 不再通过未勾选状态撤销已有授权。

## [Unreleased] - 2026-11-19

### Fixed

- 修复注册锁超时错误分类、Marker-only 注册参数语义、创建权限前端测试缺口，以及 Quickstart 复用成员时误撤销创建权限的问题。
