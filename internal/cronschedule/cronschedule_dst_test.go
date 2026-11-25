package cronschedule

import (
	"testing"
	"time"
)

var sweepZones = []string{
	"UTC",
	"America/New_York",
	"America/Chicago",
	"America/Denver",
	"America/Los_Angeles",
	"Europe/London",
	"Europe/Berlin",
	"Europe/Dublin",
	"Australia/Sydney",
	"Australia/Lord_Howe",
	"Pacific/Chatham",
	"America/Santiago",
	"America/Havana",
	"Asia/Tehran",
}

var fixedDailySchedules = []string{"0 0 * * *", "0 12 * * *", "30 2 * * *", "15 2 * * *", "0 3 * * *"}

func TestNextFiresFixedSchedulesOncePerLocalDayAcrossTransitions(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, zone := range sweepZones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatalf("load %s: %v", zone, err)
		}
		for _, expr := range fixedDailySchedules {
			dates := firedLocalDates(t, expr, zone, loc, from, to)
			if len(dates) == 0 {
				t.Fatalf("%s %s: never fired", zone, expr)
			}
			for i, date := range dates {
				if i == 0 {
					continue
				}
				gap := int(date.Sub(dates[i-1]).Hours() / 24)
				if gap == 0 {
					t.Fatalf("%s %s: fired twice on %s", zone, expr, date.Format("2006-01-02"))
				}
				if gap > 1 {
					t.Fatalf("%s %s: skipped a day between %s and %s",
						zone, expr, dates[i-1].Format("2006-01-02"), date.Format("2006-01-02"))
				}
			}
		}
	}
}

func TestNextFiresFixedWeeklyScheduleOncePerWeekAcrossTransitions(t *testing.T) {
	const expr = "0 3 * * 1"
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, zone := range sweepZones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatalf("load %s: %v", zone, err)
		}
		dates := firedLocalDates(t, expr, zone, loc, from, to)
		for i, date := range dates {
			if date.Weekday() != time.Monday {
				t.Fatalf("%s %s: fired on %s", zone, expr, date.Weekday())
			}
			if i == 0 {
				continue
			}
			if days := int(date.Sub(dates[i-1]).Hours() / 24); days != 7 {
				t.Fatalf("%s %s: %d days between %s and %s", zone, expr, days,
					dates[i-1].Format("2006-01-02"), date.Format("2006-01-02"))
			}
		}
	}
}

func TestNextIsStrictlyMonotonicAroundTransitions(t *testing.T) {
	exprs := []string{"0 0 * * *", "0 3 * * *", "30 2 * * *", "15 2 * * *", "0 12 * * *", "*/15 * * * *", "0 * * * *"}
	for _, zone := range sweepZones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatalf("load %s: %v", zone, err)
		}
		for _, transition := range transitionsIn(loc, 2026) {
			for _, expr := range exprs {
				after := transition.Add(-12 * time.Hour)
				for after.Before(transition.Add(12 * time.Hour)) {
					next, err := Next(expr, zone, after)
					if err != nil {
						t.Fatalf("%s %s: %v", zone, expr, err)
					}
					if !next.After(after) {
						t.Fatalf("%s %s: Next(%v) = %v is not after its input",
							zone, expr, after, next)
					}
					after = after.Add(time.Minute)
				}
			}
		}
	}
}

func TestNextMatchesRobfigForWildcardHourSchedules(t *testing.T) {
	zones := []string{
		"UTC", "America/New_York", "America/Chicago", "America/Los_Angeles",
		"Europe/London", "Europe/Berlin", "Australia/Sydney", "America/Santiago", "Asia/Tehran",
	}
	exprs := []string{
		"0 * * * *", "*/15 * * * *", "*/30 * * * *", "17 * * * *",
		"*/5 * * * *", "0,30 * * * *", "30 * * * *", "0 */2 * * *",
	}
	for _, zone := range zones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatalf("load %s: %v", zone, err)
		}
		for _, expr := range exprs {
			schedule, err := parser.Parse(expr)
			if err != nil {
				t.Fatalf("parse %q: %v", expr, err)
			}
			check := func(after time.Time) {
				got, err := Next(expr, zone, after)
				if err != nil {
					t.Fatalf("%s %s: %v", zone, expr, err)
				}
				want := schedule.Next(after.In(loc)).UTC()
				if !got.Equal(want) {
					t.Fatalf("%s %s after %v: got %v, robfig/cron %v",
						zone, expr, after, got, want)
				}
			}
			for _, transition := range transitionsIn(loc, 2026) {
				after := transition.Add(-6 * time.Hour)
				for after.Before(transition.Add(6 * time.Hour)) {
					check(after)
					after = after.Add(time.Minute)
				}
			}
			after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
			for range 1500 {
				got, err := Next(expr, zone, after)
				if err != nil {
					t.Fatalf("%s %s: %v", zone, expr, err)
				}
				if want := schedule.Next(after.In(loc)).UTC(); !got.Equal(want) {
					t.Fatalf("%s %s after %v: got %v, robfig/cron %v", zone, expr, after, got, want)
				}
				after = got
			}
		}
	}
}

func TestNextHandlesSubHourOffsetZones(t *testing.T) {
	t.Run("Lord_Howe daily noon never skips a day", func(t *testing.T) {
		loc, _ := time.LoadLocation("Australia/Lord_Howe")
		dates := firedLocalDates(t, "0 12 * * *", "Australia/Lord_Howe", loc,
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
		for i := 1; i < len(dates); i++ {
			if int(dates[i].Sub(dates[i-1]).Hours()/24) != 1 {
				t.Fatalf("gap between %s and %s", dates[i-1], dates[i])
			}
		}
		for _, inst := range firedInstants(t, "0 12 * * *", "Australia/Lord_Howe",
			time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)) {
			if h := inst.In(loc).Hour(); h != 12 {
				t.Fatalf("noon schedule fired at hour %d (%v)", h, inst.In(loc))
			}
		}
	})

	t.Run("Chatham fixed-hour schedule stays strictly monotonic through fall-back", func(t *testing.T) {
		loc, _ := time.LoadLocation("Pacific/Chatham")
		after := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
		end := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
		for after.Before(end) {
			next, err := Next("0 3 * * *", "Pacific/Chatham", after)
			if err != nil {
				t.Fatal(err)
			}
			if !next.After(after) {
				t.Fatalf("Next(%v) = %v is not after its input", after, next)
			}
			after = after.Add(time.Minute)
		}
		dates := firedLocalDates(t, "0 3 * * *", "Pacific/Chatham", loc,
			time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 9, 0, 0, 0, 0, time.UTC))
		for i := 1; i < len(dates); i++ {
			if int(dates[i].Sub(dates[i-1]).Hours()/24) != 1 {
				t.Fatalf("Chatham 0 3 fired irregularly: %s then %s", dates[i-1], dates[i])
			}
		}
	})
}

func firedLocalDates(t *testing.T, expr, zone string, loc *time.Location, from, to time.Time) []time.Time {
	t.Helper()
	var dates []time.Time
	for _, inst := range firedInstants(t, expr, zone, from, to) {
		local := inst.In(loc)
		dates = append(dates, time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC))
	}
	return dates
}

func firedInstants(t *testing.T, expr, zone string, from, to time.Time) []time.Time {
	t.Helper()
	var fired []time.Time
	after := from
	for {
		next, err := Next(expr, zone, after)
		if err != nil {
			t.Fatalf("%s %s: %v", zone, expr, err)
		}
		if !next.After(after) {
			t.Fatalf("%s %s: Next(%v) = %v is not after its input", zone, expr, after, next)
		}
		if !next.Before(to) {
			return fired
		}
		fired = append(fired, next)
		after = next.Add(time.Minute)
	}
}

func transitionsIn(loc *time.Location, year int) []time.Time {
	var out []time.Time
	t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := offsetAt(loc, t)
	for t.Before(end) {
		next := t.Add(time.Hour)
		if off := offsetAt(loc, next); off != prev {
			for m := t; !m.After(next); m = m.Add(time.Minute) {
				if offsetAt(loc, m) != prev {
					out = append(out, m)
					break
				}
			}
			prev = off
		}
		t = next
	}
	return out
}
