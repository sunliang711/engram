package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"engram/internal/safekeep"

	"github.com/google/uuid"
)

const TaskSafekeepMigrationBackup = "safekeep_migration_backup"

// MigrationArchiveRequest specifies how the scheduled backup task exports an archive.
type MigrationArchiveRequest struct {
	Mode           string
	IncludeDeleted bool
	Passphrase     string
}

// MigrationArchiveResult contains an exported Engram migration archive and summary counts.
type MigrationArchiveResult struct {
	Filename  string
	Data      []byte
	Format    string
	Encrypted bool
	Tenants   int
	Projects  int
	Memories  int
}

// MigrationArchiveFunc builds a deterministic migration archive for scheduled backup.
type MigrationArchiveFunc func(ctx context.Context, req MigrationArchiveRequest) (*MigrationArchiveResult, error)

// RegisterSafekeepBackupTask registers the built-in scheduled task that exports migration archives to SafeKeep.
func (s *Scheduler) RegisterSafekeepBackupTask(skSvc *safekeep.Service, exportFn MigrationArchiveFunc) error {
	defaultInterval := s.cfg.SafekeepBackupInterval
	if defaultInterval < minTaskInterval {
		defaultInterval = 24 * time.Hour
	}

	return s.Register(TaskSpec{
		Name:            TaskSafekeepMigrationBackup,
		DisplayName:     "定时导出迁移包到 SafeKeep",
		Description:     "按配置的执行间隔自动导出 Engram 系统或知识库迁移包，并通过 SafeKeep API 上传至远程存储。",
		TaskType:        "builtin",
		DefaultEnabled:  s.cfg.SafekeepBackupEnabled,
		DefaultInterval: defaultInterval,
		DefaultConfig: map[string]any{
			"migration_mode":  "system",
			"include_deleted": true,
			"upload_mode":     "",
			"remote_path":     "",
			"targets":         "",
		},
		ValidateConfig: validateSafekeepBackupConfig,
		Run: func(ctx context.Context, cfg map[string]any) (TaskResult, error) {
			if skSvc == nil {
				return TaskResult{}, fmt.Errorf("safekeep service not initialized")
			}
			if exportFn == nil {
				return TaskResult{}, fmt.Errorf("migration exporter not initialized")
			}
			validated, err := validateSafekeepBackupConfig(cfg)
			if err != nil {
				return TaskResult{}, err
			}

			migMode := validated["migration_mode"].(string)
			includeDeleted := validated["include_deleted"].(bool)
			uploadMode := validated["upload_mode"].(string)
			remotePath := validated["remote_path"].(string)
			targets := validated["targets"].(string)

			passphrase, err := skSvc.BackupPassphrase(ctx)
			if err != nil {
				return TaskResult{}, err
			}

			archive, err := exportFn(ctx, MigrationArchiveRequest{
				Mode:           migMode,
				IncludeDeleted: includeDeleted,
				Passphrase:     passphrase,
			})
			if err != nil {
				return TaskResult{}, err
			}

			res, err := skSvc.UploadArchive(ctx, archive.Filename, archive.Data, safekeep.UploadOptions{
				Mode:           uploadMode,
				Path:           remotePath,
				Targets:        targets,
				IdempotencyKey: "engram-sched-" + uuid.NewString(),
				Metadata: map[string]any{
					"source":         "engram",
					"migration_mode": migMode,
					"format":         archive.Format,
					"encrypted":      archive.Encrypted,
					"tenants":        archive.Tenants,
					"projects":       archive.Projects,
					"memories":       archive.Memories,
				},
			})
			if err != nil {
				return TaskResult{}, err
			}

			summary := fmt.Sprintf("已导出 %s（%d 条知识，%d 字节）并上传至 SafeKeep（任务 %s，状态 %s）",
				archive.Filename, archive.Memories, len(archive.Data), res.TaskID, res.Status)
			return TaskResult{
				AffectedRows: int64(archive.Memories),
				Summary:      summary,
				Details: map[string]any{
					"filename":        archive.Filename,
					"file_size":       len(archive.Data),
					"migration_mode":  migMode,
					"encrypted":       archive.Encrypted,
					"tenants":         archive.Tenants,
					"projects":        archive.Projects,
					"memories":        archive.Memories,
					"safekeep_task":   res.TaskID,
					"safekeep_status": res.Status,
					"safekeep_mode":   res.Mode,
					"deduplicated":    res.Deduplicated,
				},
			}, nil
		},
	})
}

func validateSafekeepBackupConfig(raw map[string]any) (map[string]any, error) {
	migMode := "system"
	if v, ok := raw["migration_mode"].(string); ok && strings.TrimSpace(v) != "" {
		migMode = strings.ToLower(strings.TrimSpace(v))
	}
	if migMode != "system" && migMode != "knowledge" {
		return nil, fmt.Errorf("migration_mode must be 'system' or 'knowledge'")
	}

	includeDeleted := true
	if v, exists := raw["include_deleted"]; exists && v != nil {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("include_deleted must be a boolean")
		}
		includeDeleted = b
	}

	uploadMode := ""
	if v, ok := raw["upload_mode"].(string); ok {
		uploadMode = strings.ToLower(strings.TrimSpace(v))
	}
	if uploadMode != "" && uploadMode != "sync" && uploadMode != "async" {
		return nil, fmt.Errorf("upload_mode must be '', 'sync', or 'async'")
	}

	remotePath := ""
	if v, ok := raw["remote_path"].(string); ok {
		remotePath = strings.Trim(strings.TrimSpace(v), "/")
	}
	if strings.Contains(remotePath, "..") {
		return nil, fmt.Errorf("remote_path must not contain '..'")
	}

	targets := ""
	if v, ok := raw["targets"].(string); ok {
		targets = strings.TrimSpace(v)
	}

	return map[string]any{
		"migration_mode":  migMode,
		"include_deleted": includeDeleted,
		"upload_mode":     uploadMode,
		"remote_path":     remotePath,
		"targets":         targets,
	}, nil
}
