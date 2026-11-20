package scheduler

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"engram/internal/audit"
	"engram/internal/config"
	"engram/internal/db"
	"engram/internal/observability"
)

// TestPreviewScheduleUsesExecutionRules verifies previews share execution semantics for dates and intervals.
func TestPreviewScheduleUsesExecutionRules(t *testing.T) {
	after := time.Date(2027, 1, 31, 3, 0, 0, 0, time.UTC)
	for _, req := range []SchedulePreviewRequest{
		{ScheduleType: "monthly", CronExpression: "0 2 31 * *", Timezone: "Asia/Singapore"},
		{ScheduleType: "cron", CronExpression: "*/15 9-17 * * MON-FRI", Timezone: "UTC"},
		{ScheduleType: "interval", Interval: "30m"},
	} {
		times, err := PreviewSchedule(req, after)
		if err != nil || len(times) != 5 {
			t.Fatalf("preview %+v: times=%v err=%v", req, times, err)
		}
		for _, next := range times {
			if !next.After(after) {
				t.Fatal("preview dates must be strictly increasing")
			}
			if req.ScheduleType == "monthly" && next.In(time.FixedZone("SG", 8*3600)).Day() != 31 {
				t.Fatal("monthly preview must skip absent dates")
			}
			after = next
		}
	}
	for _, req := range []SchedulePreviewRequest{
		{CronExpression: "bad"}, {CronExpression: "0 2 * * *", Timezone: "bad/zone"},
		{ScheduleType: "interval", Interval: "1s"}, {ScheduleType: "interval", Interval: "999999999d"},
	} {
		if _, err := PreviewSchedule(req, after); err == nil {
			t.Fatalf("invalid preview accepted: %+v", req)
		}
	}
}

// TestTaskTimeoutPersists verifies cancellation reaches task code and timeout outcomes survive a reload.
func TestTaskTimeoutPersists(t *testing.T) {
	dsn := os.Getenv("ENGRAM_SCHEDULER_TEST_DSN")
	if dsn == "" {
		t.Skip("ENGRAM_SCHEDULER_TEST_DSN is required for scheduler timeout tests")
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
	if err := s.Register(TaskSpec{Name: "timeout_test", DefaultInterval: time.Hour, Run: func(ctx context.Context, _ map[string]any) (TaskResult, error) {
		<-ctx.Done()
		return TaskResult{Summary: "task observed cancellation"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = database.Pool.Exec(ctx, `DELETE FROM scheduled_tasks WHERE name = 'timeout_test'`) })
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []int64{0, 86401} {
		if _, err := s.UpdateTask(ctx, UpdateTaskRequest{Name: "timeout_test", TimeoutSeconds: &invalid}, "test"); err == nil {
			t.Fatalf("accepted invalid timeout %d", invalid)
		}
	}
	timeout := int64(1)
	updated, err := s.UpdateTask(ctx, UpdateTaskRequest{Name: "timeout_test", TimeoutSeconds: &timeout}, "test")
	if err != nil || updated.TimeoutSeconds != timeout {
		t.Fatalf("update timeout: %+v err=%v", updated, err)
	}
	run, err := s.TriggerTask(ctx, "timeout_test", "test")
	if !errors.Is(err, context.DeadlineExceeded) || run == nil || run.Status != StatusTimeout {
		t.Fatalf("expected timeout outcome, run=%+v err=%v", run, err)
	}
	task, err := s.GetTask(ctx, "timeout_test")
	if err != nil || task.LastStatus != StatusTimeout || task.RunCount != 1 {
		t.Fatalf("persisted timeout task=%+v err=%v", task, err)
	}
	runs, total, err := s.ListRuns(ctx, "timeout_test", 1, 20)
	if err != nil || total != 1 || len(runs) != 1 || runs[0].Status != StatusTimeout {
		t.Fatalf("timeout history=%+v total=%d err=%v", runs, total, err)
	}
	if mapRunStatusToAudit(StatusTimeout) != audit.StatusFailed {
		t.Fatal("timeout must be a failed audit outcome")
	}
}
