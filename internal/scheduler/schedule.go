package scheduler

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"engram/internal/config"

	"github.com/robfig/cron/v3"
)

const defaultScheduleTimezone = "Asia/Singapore"

// SchedulePreviewRequest uses the same schedule fields as task updates without changing persisted state.
type SchedulePreviewRequest struct {
	ScheduleType    string `json:"schedule_type"`
	CronExpression  string `json:"cron_expression"`
	Timezone        string `json:"timezone"`
	Interval        string `json:"interval"`
	IntervalSeconds int64  `json:"interval_seconds"`
}

// PreviewSchedule validates a draft and returns its next five occurrences using the execution calculator.
func PreviewSchedule(req SchedulePreviewRequest, after time.Time) ([]time.Time, error) {
	kind := strings.TrimSpace(req.ScheduleType)
	if kind == "" {
		kind = "cron"
	}
	zone := strings.TrimSpace(req.Timezone)
	if zone == "" {
		zone = defaultScheduleTimezone
	}
	interval := req.IntervalSeconds
	if kind == "interval" && strings.TrimSpace(req.Interval) != "" {
		duration, err := config.ParseDuration(req.Interval)
		if err != nil || duration < minTaskInterval || duration > maxTaskInterval {
			return nil, fmt.Errorf("invalid interval_seconds")
		}
		interval = int64(duration.Seconds())
	}
	out := make([]time.Time, 0, 5)
	for range 5 {
		next, err := nextScheduledRun(kind, req.CronExpression, zone, interval, after)
		if err != nil {
			return nil, err
		}
		out = append(out, next)
		after = next
	}
	return out, nil
}

// nextScheduledRun computes calendar occurrences without converting months into durations.
func nextScheduledRun(scheduleType, expression, timezone string, intervalSeconds int64, after time.Time) (time.Time, error) {
	if scheduleType == "interval" {
		if intervalSeconds < int64(minTaskInterval.Seconds()) || intervalSeconds > int64(maxTaskInterval.Seconds()) {
			return time.Time{}, fmt.Errorf("invalid interval_seconds")
		}
		return after.Add(time.Duration(intervalSeconds) * time.Second), nil
	}
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron expression must contain five fields: minute hour day month weekday")
	}
	// Presets use a restricted shape so the UI label always matches the stored rule.
	validShape := false
	switch scheduleType {
	case "every_minute":
		validShape = strings.Join(fields, " ") == "* * * * *"
	case "every_5_minutes":
		validShape = strings.Join(fields, " ") == "*/5 * * * *"
	case "every_10_minutes":
		validShape = strings.Join(fields, " ") == "*/10 * * * *"
	case "hourly":
		validShape = strings.Join(fields[1:], " ") == "* * * *" && isCronNumber(fields[0])
	case "daily":
		validShape = strings.Join(fields[2:], " ") == "* * *" && isCronNumber(fields[0]) && isCronNumber(fields[1])
	case "weekly":
		validShape = fields[2] == "*" && fields[3] == "*" && isCronNumber(fields[0]) && isCronNumber(fields[1]) && isCronNumber(fields[4])
	case "monthly":
		validShape = fields[3] == "*" && fields[4] == "*" && isCronNumber(fields[0]) && isCronNumber(fields[1]) && isCronNumber(fields[2])
	case "cron":
		validShape = true
	}
	if !validShape {
		return time.Time{}, fmt.Errorf("cron expression does not match schedule_type")
	}
	if _, err := time.LoadLocation(timezone); err != nil || timezone == "" || timezone == "Local" {
		return time.Time{}, fmt.Errorf("invalid schedule timezone")
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("CRON_TZ=" + timezone + " " + strings.Join(fields, " "))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	next := schedule.Next(after)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression has no occurrence in the next five years")
	}
	return next.UTC(), nil
}

// isCronNumber distinguishes simple preset values from custom cron syntax.
func isCronNumber(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
