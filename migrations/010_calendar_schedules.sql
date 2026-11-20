-- Preserve existing interval tasks while adding calendar schedules and explicit time zones.
ALTER TABLE scheduled_tasks
    ADD COLUMN IF NOT EXISTS schedule_type TEXT NOT NULL DEFAULT 'interval',
    ADD COLUMN IF NOT EXISTS cron_expression TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'Asia/Singapore';
