-- System settings are the sole source of scheduled SafeKeep upload parameters.
-- Keep schedule, export scope and task history intact when removing legacy overrides.
UPDATE scheduled_tasks
SET config_json = config_json - 'upload_mode' - 'remote_path' - 'targets',
    updated_at = now()
WHERE name = 'safekeep_migration_backup'
  AND config_json ?| ARRAY['upload_mode', 'remote_path', 'targets'];
