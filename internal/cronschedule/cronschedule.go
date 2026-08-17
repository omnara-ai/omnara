package cronschedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

func parseExpression(expression string) (cron.Schedule, error) {
	trimmed := strings.TrimSpace(expression)
	if strings.HasPrefix(trimmed, "TZ=") || strings.HasPrefix(trimmed, "CRON_TZ=") {
		return nil, errors.New("timezone prefixes are not supported; use the timezone field")
	}
	return parser.Parse(expression)
}

func Validate(expression, timezone string) error {
	if _, err := parseExpression(expression); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	if timezone == "" || timezone == "Local" {
		return fmt.Errorf("invalid timezone: %q", timezone)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	return nil
}

func Next(expression, timezone string, after time.Time) (time.Time, error) {
	schedule, err := parseExpression(expression)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone: %w", err)
	}
	next := schedule.Next(after.In(location))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q never fires", expression)
	}
	return next.UTC(), nil
}
