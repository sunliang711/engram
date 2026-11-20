# SafeKeep 备份开关统一

## 问题与根因

系统设置中的 SafeKeep 集成开关与定时任务中的启用开关分别存储。
上传服务还会检查集成开关，导致用户开启定时任务后仍可能因集成关闭而备份失败。

## 修复结果

- 系统设置移除集成开关，状态标记改为根据 URL 和已保存 API Key 显示“已配置／未配置”。
- 自动备份由 `safekeep_migration_backup` 定时任务启停，仍受调度器全局启用配置约束。
- 手动备份只要求连接配置完整，不受定时任务启停影响。
- 上传仍校验 URL、API Key 和凭据解密能力；保留既有密钥加密、管理接口鉴权和操作审计。
- 清除 API Key 不再提交集成启停状态，上传时返回配置缺失提示。

## 文件与配置变更

- `internal/safekeep/`：移除设置模型、读写及上传校验中的集成启用字段；更新服务集成测试。
- `internal/config/config.go`、`config.example.toml`：移除集成启用配置，保留定时备份启用配置。
- `internal/admin/safekeep.go`：设置更新审计不再记录集成启用字段。
- `internal/admin/ui/`、`internal/admin/uitest/audit-scheduler.test.cjs`：更新设置表单、状态、定时任务提示、中英文文案与交互测试。
- `README.md`、`DESIGN.md`：同步使用方式与兼容说明。

## 兼容与部署

不需要数据库迁移，旧表的 `safekeep_settings.enabled` 字段保留但应用不再读写。
旧 `[safekeep].enabled`、`ENGRAM_SAFEKEEP_ENABLED` 及设置请求中的 `enabled` 均不再生效，设置响应不再返回 `enabled`。
更新服务后，如果原先定时任务已启用且 URL、API Key 配置完整，即使旧集成开关关闭也会允许备份上传。
停止自动备份应关闭定时任务。

## 验证结果

- `go test -race ./...`：通过；未提供 DSN 的数据库测试按现有规则跳过。
- SafeKeep 包使用独立临时 PostgreSQL 数据库运行 `go test -race -count=1 -v ./internal/safekeep`：通过，覆盖配置初始化、凭据保存、旧开关两种取值下上传成功及清除密钥后拒绝上传。
- 管理接口使用另一个独立临时 PostgreSQL 数据库运行 `go test -race -count=1 -v ./internal/admin -run TestAdminSafekeepSettingsAndScheduledMigrationBackup`：通过，覆盖无集成开关的配置保存、任务启停、任务触发、加密迁移包上传及停用自动备份后的手动上传。
- `node --test internal/admin/uitest/*.test.cjs`：48 项通过，覆盖配置状态、中英文提示、保存与清除密钥请求。
- `git diff --check`：通过。
- 临时 PostgreSQL 实例已停止；未连接真实 SafeKeep 服务，上传使用本地 HTTP 测试服务验证。

## ChangeLog

### Fixed — 2026-11-20

- SafeKeep：移除系统设置中的独立集成开关，解决定时任务已启用但上传仍被另一开关阻止的问题。
