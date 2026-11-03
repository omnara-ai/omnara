package executionstore

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func loadUsageSeriesLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return location
}

func assertUsageBucketStarts(t *testing.T, got []time.Time, want ...time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("bucket starts = %v, want %v", got, want)
	}
	for index := range want {
		if !got[index].Equal(want[index]) {
			t.Fatalf("bucket start %d = %v, want %v", index, got[index], want[index])
		}
	}
}

const testUsageBucketLimit = 400

func usageBucketStarts(
	t *testing.T,
	since, until time.Time,
	interval UsageInterval,
	location *time.Location,
) []time.Time {
	t.Helper()
	starts, ok := UsageBucketStarts(since, until, interval, location, testUsageBucketLimit)
	if !ok {
		t.Fatalf("bucket starts exceeded %d", testUsageBucketLimit)
	}
	return starts
}

func TestUsageBucketStartsFollowDaylightSaving(t *testing.T) {
	losAngeles := loadUsageSeriesLocation(t, "America/Los_Angeles")
	assertUsageBucketStarts(
		t, usageBucketStarts(
			t,
			time.Date(2026, 3, 7, 9, 0, 0, 0, losAngeles),
			time.Date(2026, 3, 9, 8, 0, 0, 0, losAngeles),
			UsageIntervalDay, losAngeles,
		),
		time.Date(2026, 3, 7, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 7, 0, 0, 0, time.UTC),
	)
}

func TestUsageBucketStartsBeginDaysWhenMidnightIsSkipped(t *testing.T) {
	cairo := loadUsageSeriesLocation(t, "Africa/Cairo")
	assertUsageBucketStarts(
		t, usageBucketStarts(
			t,
			time.Date(2026, 4, 23, 6, 0, 0, 0, cairo),
			time.Date(2026, 4, 25, 6, 0, 0, 0, cairo),
			UsageIntervalDay, cairo,
		),
		time.Date(2026, 4, 22, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 23, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 24, 21, 0, 0, 0, time.UTC),
	)
}

func TestUsageBucketStartsUseTheLocalDate(t *testing.T) {
	tokyo := loadUsageSeriesLocation(t, "Asia/Tokyo")
	assertUsageBucketStarts(
		t, usageBucketStarts(
			t,
			time.Date(2026, 9, 21, 20, 0, 0, 0, time.UTC),
			time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC),
			UsageIntervalDay, tokyo,
		),
		time.Date(2026, 9, 21, 15, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC),
	)
}

func TestUsageBucketStartsAlignWeeksToMonday(t *testing.T) {
	assertUsageBucketStarts(
		t, usageBucketStarts(
			t,
			time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC),
			UsageIntervalWeek, time.UTC,
		),
		time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
	)
}

func TestUsageBucketStartsAlignMonthsAcrossYears(t *testing.T) {
	assertUsageBucketStarts(
		t, usageBucketStarts(
			t,
			time.Date(2025, 11, 30, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UsageIntervalMonth, time.UTC,
		),
		time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
	)
}

func TestUsageBucketStartsRejectTooManyBuckets(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, ok := UsageBucketStarts(since, since.AddDate(0, 0, 3), UsageIntervalDay, time.UTC, 2); ok {
		t.Fatal("expected three daily buckets to exceed a limit of two")
	}
	starts, ok := UsageBucketStarts(since, since.AddDate(0, 0, 2), UsageIntervalDay, time.UTC, 2)
	if !ok || len(starts) != 2 {
		t.Fatalf("bucket starts = %v, %v; want two buckets", starts, ok)
	}
}

func TestModelUsageSeriesFilterValidation(t *testing.T) {
	until := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for name, filter := range map[string]ModelUsageSeriesFilter{
		"projects without orgs": {ProjectIDs: []uuid.UUID{uuid.New()}, Until: until},
		"missing until":         {OrgIDs: []uuid.UUID{uuid.New()}},
		"since not before end":  {OrgIDs: []uuid.UUID{uuid.New()}, Since: &until, Until: until},
	} {
		if err := filter.validate(); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
	if err := (ModelUsageSeriesFilter{OrgIDs: []uuid.UUID{uuid.New()}, Until: until}).validate(); err != nil {
		t.Fatalf("valid filter: %v", err)
	}
	// A caller without memberships has no orgs and no projects: an empty, valid scope.
	if err := (ModelUsageSeriesFilter{Until: until}).validate(); err != nil {
		t.Fatalf("empty scope: %v", err)
	}
}

func TestUsageBucketStartsKeepHoursWholeAcrossDaylightSaving(t *testing.T) {
	losAngeles := loadUsageSeriesLocation(t, "America/Los_Angeles")
	assertUsageBucketStarts(
		t, usageBucketStarts(
			t,
			time.Date(2026, 11, 1, 7, 45, 0, 0, time.UTC),
			time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC),
			UsageIntervalHour, losAngeles,
		),
		time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC),
	)
}

func TestUsageBucketStartsAlignHoursToTheLocalHour(t *testing.T) {
	kolkata := loadUsageSeriesLocation(t, "Asia/Kolkata")
	assertUsageBucketStarts(
		t, usageBucketStarts(
			t,
			time.Date(2026, 9, 21, 10, 15, 0, 0, time.UTC),
			time.Date(2026, 9, 21, 11, 45, 0, 0, time.UTC),
			UsageIntervalHour, kolkata,
		),
		time.Date(2026, 9, 21, 9, 30, 0, 0, time.UTC),
		time.Date(2026, 9, 21, 10, 30, 0, 0, time.UTC),
		time.Date(2026, 9, 21, 11, 30, 0, 0, time.UTC),
	)
}

func TestUsageBucketStartsRejectTooManyHours(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, ok := UsageBucketStarts(since, since.Add(3*time.Hour), UsageIntervalHour, time.UTC, 2); ok {
		t.Fatal("expected three hourly buckets to exceed a limit of two")
	}
	starts, ok := UsageBucketStarts(since, since.Add(2*time.Hour), UsageIntervalHour, time.UTC, 2)
	if !ok || len(starts) != 2 {
		t.Fatalf("bucket starts = %v, %v; want two buckets", starts, ok)
	}
}
