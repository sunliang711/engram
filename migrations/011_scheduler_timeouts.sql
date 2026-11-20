-- Give every task a bounded execution context and preserve timeout outcomes in history.
ALTER TABLE scheduled_tasks
    ADD COLUMN IF NOT EXISTS timeout_seconds BIGINT NOT NULL DEFAULT 600
        CHECK (timeout_seconds BETWEEN 1 AND 86400);

ALTER TABLE scheduled_tasks DROP CONSTRAINT IF EXISTS scheduled_tasks_last_status_check;
ALTER TABLE scheduled_tasks ADD CONSTRAINT scheduled_tasks_last_status_check
    CHECK (last_status IN ('idle', 'running', 'success', 'failed', 'timeout'));

ALTER TABLE scheduled_task_runs DROP CONSTRAINT IF EXISTS scheduled_task_runs_status_check;
ALTER TABLE scheduled_task_runs ADD CONSTRAINT scheduled_task_runs_status_check
    CHECK (status IN ('running', 'success', 'failed', 'timeout'));
