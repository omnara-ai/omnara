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
	if _, err := LoadLocation(timezone); err != nil {
		return err
	}
	return nil
}

func LoadLocation(timezone string) (*time.Location, error) {
	if timezone == "" || timezone == "Local" {
		return nil, fmt.Errorf("invalid timezone: %q", timezone)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone: %w", err)
	}
	return location, nil
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
	next, ok := nextFire(schedule, location, after, !hourFieldIsWildcard(expression))
	if !ok {
		return time.Time{}, fmt.Errorf("cron expression %q never fires", expression)
	}
	return next.UTC(), nil
}

const (
	probeWindow = 24 * time.Hour
	// foldBackScan lets the walk reach a fall-back's earlier-reading second occurrence.
	foldBackScan = 3 * time.Hour
	// stopMargin caps the walk past the best candidate (a wall clock leads its instant by <= the max UTC offset).
	stopMargin = 15 * time.Hour
)

func hourFieldIsWildcard(expression string) bool {
	fields := strings.Fields(expression)
	if len(fields) < 2 {
		return false
	}
	return strings.ContainsAny(fields[1], "*?")
}

func nextFire(schedule cron.Schedule, loc *time.Location, after time.Time, fixedHour bool) (time.Time, bool) {
	local := after.In(loc)
	afterWall := time.Date(
		local.Year(), local.Month(), local.Day(),
		local.Hour(), local.Minute(), local.Second(), 0, time.UTC,
	)
	yearLimit := local.Year() + 5

	var best time.Time
	haveBest := false
	for cand := schedule.Next(afterWall.Add(-foldBackScan)); !cand.IsZero(); cand = schedule.Next(cand) {
		if cand.Year() > yearLimit {
			break
		}
		for _, inst := range occurrences(loc, cand, fixedHour) {
			if inst.After(after) && (!haveBest || inst.Before(best)) {
				best, haveBest = inst, true
			}
		}
		if haveBest && cand.Add(-stopMargin).After(best) {
			break
		}
	}
	return best, haveBest
}

func occurrences(loc *time.Location, naive time.Time, fixedHour bool) []time.Time {
	offBefore := offsetAt(loc, naive.Add(-probeWindow))
	offAfter := offsetAt(loc, naive.Add(probeWindow))
	candBefore := naive.Add(-time.Duration(offBefore) * time.Second)
	if offBefore == offAfter {
		return []time.Time{candBefore}
	}
	candAfter := naive.Add(-time.Duration(offAfter) * time.Second)
	validBefore := offsetAt(loc, candBefore) == offBefore
	validAfter := offsetAt(loc, candAfter) == offAfter
	switch {
	case validBefore && validAfter:
		if fixedHour {
			return []time.Time{candBefore}
		}
		return []time.Time{candBefore, candAfter}
	case validBefore:
		return []time.Time{candBefore}
	case validAfter:
		return []time.Time{candAfter}
	default:
		if !fixedHour {
			return nil
		}
		start, _ := candBefore.In(loc).ZoneBounds()
		return []time.Time{start}
	}
}

func offsetAt(loc *time.Location, t time.Time) int {
	_, offset := t.In(loc).Zone()
	return offset
}
