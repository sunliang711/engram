package scheduler

import (
	"context"
	"os"
	"testing"
	"time"

	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"
)

// TestNextScheduledRun covers real calendar dates, explicit zones and daylight saving changes.
func TestNextScheduledRun(t *testing.T) {
	cases := []struct {
		name, kind, expression, zone, after, want string
	}{
		{"every_minute", "every_minute", "* * * * *", "Asia/Singapore", "2026-11-20T03:00:00Z", "2026-11-20T03:01:00Z"},
		{"every_5_minutes", "every_5_minutes", "*/5 * * * *", "Asia/Singapore", "2026-11-20T03:01:00Z", "2026-11-20T03:05:00Z"},
		{"every_10_minutes", "every_10_minutes", "*/10 * * * *", "Asia/Singapore", "2026-11-20T03:01:00Z", "2026-11-20T03:10:00Z"},
		{"hourly", "hourly", "30 * * * *", "Asia/Singapore", "2026-11-20T03:00:00Z", "2026-11-20T03:30:00Z"},
		{"daily midnight", "daily", "0 0 * * *", "Asia/Singapore", "2026-11-20T03:00:00Z", "2026-11-20T16:00:00Z"},
		{"daily", "daily", "0 2 * * *", "Asia/Singapore", "2026-11-20T03:00:00Z", "2026-11-20T18:00:00Z"},
		{"weekly", "weekly", "0 2 * * 1", "Asia/Singapore", "2026-11-20T03:00:00Z", "2026-11-22T18:00:00Z"},
		{"short month", "monthly", "0 2 31 * *", "Asia/Singapore", "2027-01-31T03:00:00Z", "2027-03-30T18:00:00Z"},
		{"leap year", "cron", "0 0 29 2 *", "UTC", "2027-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"steps and weekdays", "cron", "*/15 9-17 * * MON-FRI", "UTC", "2026-11-20T09:01:00Z", "2026-11-20T09:15:00Z"},
		{"DST gap", "daily", "30 2 * * *", "America/New_York", "2026-03-08T06:59:00Z", "2026-03-09T06:30:00Z"},
		{"DST repeated hour", "daily", "30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
		{"strictly after occurrence", "daily", "0 2 * * *", "UTC", "2026-11-20T02:00:00Z", "2026-11-21T02:00:00Z"},
		{"legacy interval", "interval", "", "Asia/Singapore", "2026-11-20T03:00:00Z", "2026-11-20T04:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after, err := time.Parse(time.RFC3339, tc.after)
			if err != nil {
				t.Fatal(err)
			}
			got, err := nextScheduledRun(tc.kind, tc.expression, tc.zone, 3600, after)
			if err != nil || got.Format(time.RFC3339) != tc.want {
				t.Fatalf("next=%s err=%v, want %s", got.Format(time.RFC3339), err, tc.want)
			}
		})
	}
}

// TestCalendarScheduleValidation rejects unsupported rules before they enter persisted state.
func TestCalendarScheduleValidation(t *testing.T) {
	for _, tc := range []struct{ kind, expr, zone string }{
		{"cron", "0 0 0 * * *", "UTC"}, {"cron", "60 2 * * *", "UTC"},
		{"cron", "0 2 * * *", "bad/zone"}, {"cron", "0 2 * * *", "Local"},
		{"cron", "0 0 31 2 *", "UTC"}, {"cron", "@hourly", "UTC"},
		{"every_minute", "*/5 * * * *", "UTC"}, {"every_5_minutes", "*/10 * * * *", "UTC"},
		{"every_10_minutes", "0 * * * *", "UTC"},
		{"daily", "0 2 * * 1", "UTC"}, {"monthly", "0 2 */2 * *", "UTC"},
		{"unknown", "0 2 * * *", "UTC"},
	} {
		if _, err := nextScheduledRun(tc.kind, tc.expr, tc.zone, 3600, time.Now()); err == nil {
			t.Fatalf("expected rejection: %+v", tc)
		}
	}
}

// TestCalendarSchedulePersistenceAndExecution verifies scheduler reads, toggles and reschedules calendar tasks.
func TestCalendarSchedulePersistenceAndExecution(t *testing.T) {
	dsn := os.Getenv("ENGRAM_SCHEDULER_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_SCHEDULER_TEST_DSN is required for calendar PostgreSQL tests")
	}
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = dsn
	database, err := db.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s := New(database.Pool, cfg.Scheduler, nil, nil, observability.NewLogger("error", nil))
	runs := 0
	var onRun func() error
	if err := s.Register(TaskSpec{Name: "calendar_test", DefaultInterval: time.Hour, Run: func(context.Context, map[string]any) (TaskResult, error) {
		runs++
		if onRun != nil {
			return TaskResult{Summary: "updated during run"}, onRun()
		}
		return TaskResult{Summary: "calendar run"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM scheduled_tasks WHERE name = 'calendar_test'`) })
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.GetTask(ctx, "calendar_test")
	if err != nil || legacy.ScheduleType != "interval" {
		t.Fatalf("legacy default: %+v err=%v", legacy, err)
	}
	kind, expression, zone, enabled := "weekly", "15 2 * * 1", "Asia/Singapore", true
	task, err := s.UpdateTask(ctx, UpdateTaskRequest{Name: "calendar_test", Enabled: &enabled, ScheduleType: &kind, CronExpression: &expression, Timezone: &zone}, "test")
	if err != nil || task.ScheduleType != kind || task.CronExpression != expression || task.Timezone != zone || task.NextRunAt == nil {
		t.Fatalf("saved calendar task: %+v err=%v", task, err)
	}
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE scheduled_tasks SET next_run_at = now() - interval '1 minute' WHERE name = 'calendar_test'`); err != nil {
		t.Fatal(err)
	}
	if err := s.RunDue(ctx); err != nil || runs != 1 {
		t.Fatalf("calendar execution runs=%d err=%v", runs, err)
	}
	after, err := s.GetTask(ctx, "calendar_test")
	if err != nil || after.NextRunAt == nil || after.NextRunAt.In(time.FixedZone("SG", 8*3600)).Weekday() != time.Monday || after.NextRunAt.In(time.FixedZone("SG", 8*3600)).Hour() != 2 || after.NextRunAt.Minute() != 15 {
		t.Fatalf("calendar next occurrence: %+v err=%v", after, err)
	}
	// A stale due list on a second instance must not execute this occurrence again.
	if _, err := s.executeTask(ctx, "calendar_test", TriggerScheduled, "scheduler"); err != nil || runs != 1 {
		t.Fatalf("duplicate calendar execution runs=%d err=%v", runs, err)
	}
	enabled = false
	disabled, err := s.UpdateTask(ctx, UpdateTaskRequest{Name: "calendar_test", Enabled: &enabled}, "test")
	if err != nil || disabled.NextRunAt != nil || disabled.CronExpression != expression {
		t.Fatalf("disabled calendar task: %+v err=%v", disabled, err)
	}
	if _, err := s.TriggerTask(ctx, "calendar_test", "test"); err != nil || runs != 2 {
		t.Fatalf("manual calendar task runs=%d err=%v", runs, err)
	}
	enabled = true
	if _, err := s.UpdateTask(ctx, UpdateTaskRequest{Name: "calendar_test", Enabled: &enabled}, "test"); err != nil {
		t.Fatal(err)
	}
	// Configuration edits during an execution must retain their new next occurrence.
	var savedNext *time.Time
	onRun = func() error {
		kind, expression := "monthly", "0 4 15 * *"
		updated, err := s.UpdateTask(ctx, UpdateTaskRequest{Name: "calendar_test", ScheduleType: &kind, CronExpression: &expression}, "test")
		if err == nil {
			savedNext = updated.NextRunAt
		}
		return err
	}
	if _, err := s.TriggerTask(ctx, "calendar_test", "test"); err != nil {
		t.Fatal(err)
	}
	changed, err := s.GetTask(ctx, "calendar_test")
	if err != nil || savedNext == nil || changed.NextRunAt == nil || !changed.NextRunAt.Equal(*savedNext) || changed.ScheduleType != "monthly" {
		t.Fatalf("schedule changed during run: %+v err=%v", changed, err)
	}
	interval, err := s.UpdateTask(ctx, UpdateTaskRequest{Name: "calendar_test", Interval: "15m"}, "test")
	if err != nil || interval.ScheduleType != "interval" || interval.IntervalSeconds != 900 || interval.CronExpression != "" {
		t.Fatalf("legacy client update: %+v err=%v", interval, err)
	}
}
