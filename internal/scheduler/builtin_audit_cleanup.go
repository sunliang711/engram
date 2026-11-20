package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"engram/internal/config"
)

const (
	minAuditRetention = 1 * time.Minute
	maxAuditRetention = 3650 * 24 * time.Hour
	minBatchSize      = 100
	maxBatchSize      = 10000
)

// RegisterBuiltinTasks registers Engram's built-in scheduled tasks with the Scheduler.
func (s *Scheduler) RegisterBuiltinTasks(auditCfg config.AuditConfig) error {
	defaultRetention := auditCfg.Retention
	if defaultRetention < minAuditRetention {
		defaultRetention = 30 * 24 * time.Hour
	}
	defaultInterval := s.cfg.AuditCleanupInterval
	if defaultInterval < minTaskInterval {
		defaultInterval = time.Hour
	}
	defaultBatch := s.cfg.AuditCleanupBatchSize
	if defaultBatch < minBatchSize || defaultBatch > maxBatchSize {
		defaultBatch = 1000
	}

	return s.Register(TaskSpec{
		Name:            TaskAuditLogCleanup,
		DisplayName:     "定时清理过期审计日志",
		Description:     "按配置的保留时长（retention）和执行间隔分批清理过期的审计日志记录，防止数据库体积无限增长。",
		TaskType:        "builtin",
		DefaultEnabled:  s.cfg.AuditCleanupEnabled,
		DefaultInterval: defaultInterval,
		DefaultConfig: map[string]any{
			"retention":  formatDurationCompact(defaultRetention),
			"batch_size": defaultBatch,
		},
		ValidateConfig: validateAuditCleanupConfig,
		Run: func(ctx context.Context, cfg map[string]any) (TaskResult, error) {
			if s.auditSvc == nil {
				return TaskResult{}, fmt.Errorf("audit service not initialized")
			}
			validated, err := validateAuditCleanupConfig(cfg)
			if err != nil {
				return TaskResult{}, err
			}
			retStr := validated["retention"].(string)
			retention, err := config.ParseDuration(retStr)
			if err != nil {
				return TaskResult{}, err
			}
			batchSize := validated["batch_size"].(int)

			deleted, cutoff, err := s.auditSvc.CleanupExpired(ctx, retention, batchSize)
			if err != nil {
				return TaskResult{}, err
			}

			summary := fmt.Sprintf("已清理 %d 条过期审计日志（保留时长 %s，截止时间 %s）", deleted, retStr, cutoff.Format(time.RFC3339))
			if deleted == 0 {
				summary = fmt.Sprintf("无过期审计日志需要清理（保留时长 %s，截止时间 %s）", retStr, cutoff.Format(time.RFC3339))
			}
			return TaskResult{
				AffectedRows: deleted,
				Summary:      summary,
				Details: map[string]any{
					"retention":     retStr,
					"batch_size":    batchSize,
					"cutoff":        cutoff.Format(time.RFC3339),
					"deleted_count": deleted,
				},
			}, nil
		},
	})
}

func validateAuditCleanupConfig(raw map[string]any) (map[string]any, error) {
	retVal, ok := raw["retention"]
	if !ok {
		return nil, fmt.Errorf("retention is required (e.g. \"30d\", \"720h\", \"24h\")")
	}
	retStr, ok := retVal.(string)
	if !ok || strings.TrimSpace(retStr) == "" {
		return nil, fmt.Errorf("retention must be a non-empty duration string (e.g. \"30d\", \"720h\")")
	}
	retStr = strings.TrimSpace(retStr)
	d, err := config.ParseDuration(retStr)
	if err != nil {
		return nil, fmt.Errorf("invalid retention %q: %w", retStr, err)
	}
	if d < minAuditRetention || d > maxAuditRetention {
		return nil, fmt.Errorf("retention must be between %s and 3650d", minAuditRetention)
	}

	batchSize := 1000
	if bsRaw, exists := raw["batch_size"]; exists && bsRaw != nil {
		parsed, ok := parseIntValue(bsRaw)
		if !ok {
			return nil, fmt.Errorf("batch_size must be an integer")
		}
		if parsed < minBatchSize || parsed > maxBatchSize {
			return nil, fmt.Errorf("batch_size must be between %d and %d", minBatchSize, maxBatchSize)
		}
		batchSize = parsed
	}

	return map[string]any{
		"retention":  retStr,
		"batch_size": batchSize,
	}, nil
}

func parseIntValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func formatDurationCompact(d time.Duration) string {
	if d%(24*time.Hour) == 0 && d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	}
	return d.String()
}
