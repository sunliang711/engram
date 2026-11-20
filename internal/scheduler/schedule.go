package scheduler

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

const defaultScheduleTimezone = "Asia/Singapore"

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
