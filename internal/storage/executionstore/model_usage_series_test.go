package executionstore

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func modelenvelopeCost(t *testing.T, raw string) modelenvelope.ProviderReportedCostUSD {
	t.Helper()
	cost, ok := modelenvelope.ParseProviderReportedCostUSD(raw)
	if !ok {
		t.Fatalf("invalid cost %q", raw)
	}
	return cost
}

func loadUsageSeriesLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return location
}

func assertUsageIntervalStarts(t *testing.T, got []time.Time, want ...time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("interval starts = %v, want %v", got, want)
	}
	for index := range want {
		if !got[index].Equal(want[index]) {
			t.Fatalf("interval start %d = %v, want %v", index, got[index], want[index])
		}
	}
}

func TestUsageIntervalStartsFollowLocalDaysAcrossDaylightSaving(t *testing.T) {
	losAngeles := loadUsageSeriesLocation(t, "America/Los_Angeles")
	starts, err := UsageIntervalStarts(
		time.Date(2026, 3, 7, 10, 30, 0, 0, losAngeles),
		time.Date(2026, 3, 9, 8, 0, 0, 0, losAngeles),
		UsageIntervalDay,
		losAngeles,
	)
	if err != nil {
		t.Fatalf("interval starts: %v", err)
	}
	assertUsageIntervalStarts(
		t, starts,
		time.Date(2026, 3, 7, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 7, 0, 0, 0, time.UTC),
	)
}

func TestUsageIntervalStartsBeginDaysWhenMidnightIsSkipped(t *testing.T) {
	cairo := loadUsageSeriesLocation(t, "Africa/Cairo")
	starts, err := UsageIntervalStarts(
		time.Date(2026, 4, 23, 12, 0, 0, 0, cairo),
		time.Date(2026, 4, 25, 6, 0, 0, 0, cairo),
		UsageIntervalDay,
		cairo,
	)
	if err != nil {
		t.Fatalf("interval starts: %v", err)
	}
	assertUsageIntervalStarts(
		t, starts,
		time.Date(2026, 4, 22, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 23, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 24, 21, 0, 0, 0, time.UTC),
	)
}

func TestUsageIntervalStartsAlignHoursToLocalClock(t *testing.T) {
	kolkata := loadUsageSeriesLocation(t, "Asia/Kolkata")
	starts, err := UsageIntervalStarts(
		time.Date(2026, 9, 22, 4, 47, 12, 5, time.UTC),
		time.Date(2026, 9, 22, 6, 10, 0, 0, time.UTC),
		UsageIntervalHour,
		kolkata,
	)
	if err != nil {
		t.Fatalf("interval starts: %v", err)
	}
	assertUsageIntervalStarts(
		t, starts,
		time.Date(2026, 9, 22, 4, 30, 0, 0, time.UTC),
		time.Date(2026, 9, 22, 5, 30, 0, 0, time.UTC),
	)
}

func TestUsageIntervalStartsRejectInvalidWindows(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		until    time.Time
		interval UsageInterval
	}{
		{name: "until before since", until: since.Add(-time.Hour), interval: UsageIntervalDay},
		{name: "too many hours", until: since.Add(MaxUsageIntervals*time.Hour + time.Minute), interval: UsageIntervalHour},
		{name: "too many days", until: since.AddDate(1, 1, 0), interval: UsageIntervalDay},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := UsageIntervalStarts(since, test.until, test.interval, time.UTC); !errors.Is(
				err, storeerr.ErrInvalidRequest,
			) {
				t.Fatalf("error = %v, want invalid request", err)
			}
		})
	}
	starts, err := UsageIntervalStarts(since, since.Add(MaxUsageIntervals*time.Hour), UsageIntervalHour, time.UTC)
	if err != nil || len(starts) != MaxUsageIntervals {
		t.Fatalf("maximum window = %d starts, %v; want %d", len(starts), err, MaxUsageIntervals)
	}
}

func TestAssembleModelUsageSeriesRanksGroupsAndKeepsEmptyIntervals(t *testing.T) {
	starts := []time.Time{
		time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
	}
	light, heavy, tail := uuid.New(), uuid.New(), uuid.New()
	usage := func(input, output int64, cost string) ModelUsageTotals {
		return ModelUsageTotals{
			ModelCalls: 1, ModelCallsWithReportedCost: 1, InputTokensTotal: input, UncachedInputTokens: input,
			OutputTokensTotal: output, ProviderReportedCostUSD: modelenvelopeCost(t, cost),
		}
	}
	series, err := assembleModelUsageSeries(starts, []modelUsageSeriesRow{
		{intervalNumber: 1, groupID: light, groupName: "light", totals: usage(80, 20, "0.1")},
		{intervalNumber: 1, groupID: heavy, groupName: "heavy", totals: usage(250, 50, "0.2")},
		{intervalNumber: 3, groupID: light, groupName: "light", totals: usage(40, 10, "0.05")},
		{intervalNumber: 3, groupID: tail, groupName: "tail", totals: usage(9, 1, "0.001")},
	}, 2)
	if err != nil {
		t.Fatalf("assemble series: %v", err)
	}
	if series.Totals.ModelCalls != 4 || series.Totals.tokens() != 460 || series.Totals.ProviderReportedCostUSD != "0.351" {
		t.Fatalf("totals = %+v", series.Totals)
	}
	if len(series.Groups) != 2 || series.Groups[0].ID != heavy || series.Groups[1].ID != light ||
		series.Groups[1].Totals.tokens() != 150 || series.Groups[1].Name != "light" {
		t.Fatalf("groups = %+v, want heavy then light", series.Groups)
	}
	if len(series.Intervals) != 3 {
		t.Fatalf("intervals = %+v, want 3", series.Intervals)
	}
	first := series.Intervals[0]
	if !first.Start.Equal(starts[0]) || first.Totals.tokens() != 400 || len(first.Groups) != 2 ||
		first.Groups[0].GroupID != heavy || first.Groups[1].GroupID != light {
		t.Fatalf("first interval = %+v", first)
	}
	empty := series.Intervals[1]
	if empty.Totals.ModelCalls != 0 || empty.Totals.ProviderReportedCostUSD != "0" || len(empty.Groups) != 0 {
		t.Fatalf("empty interval = %+v", empty)
	}
	last := series.Intervals[2]
	if last.Totals.tokens() != 60 || last.Totals.ProviderReportedCostUSD != "0.051" || len(last.Groups) != 1 ||
		last.Groups[0].GroupID != light {
		t.Fatalf("last interval = %+v, want light only with tail in totals", last)
	}
}

func TestAssembleModelUsageSeriesRejectsUnknownIntervals(t *testing.T) {
	starts := []time.Time{time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)}
	for _, number := range []int32{0, 2} {
		_, err := assembleModelUsageSeries(starts, []modelUsageSeriesRow{{
			intervalNumber: number, groupID: uuid.New(), groupName: "model",
			totals: ModelUsageTotals{ModelCalls: 1, ProviderReportedCostUSD: "0"},
		}}, 5)
		if err == nil {
			t.Fatalf("interval %d assembled, want error", number)
		}
	}
}

func TestSumOrgModelUsageSeriesInputValidation(t *testing.T) {
	since := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	valid := SumOrgModelUsageSeriesInput{
		OrgID:          uuid.New(),
		Window:         UsageWindow{Since: &since},
		IntervalStarts: []time.Time{since.Truncate(24 * time.Hour), since.Truncate(24 * time.Hour).Add(24 * time.Hour)},
		GroupBy:        UsageGroupByModel,
		GroupLimit:     5,
	}
	for _, groupBy := range []UsageGroupBy{UsageGroupByModel, UsageGroupByProject, UsageGroupByProfile} {
		input := valid
		input.GroupBy = groupBy
		if err := input.validate(); err != nil {
			t.Fatalf("valid %s input: %v", groupBy, err)
		}
	}
	late := since.Add(time.Hour)
	for name, mutate := range map[string]func(*SumOrgModelUsageSeriesInput){
		"missing org":         func(input *SumOrgModelUsageSeriesInput) { input.OrgID = uuid.Nil },
		"missing since":       func(input *SumOrgModelUsageSeriesInput) { input.Window.Since = nil },
		"starts after since":  func(input *SumOrgModelUsageSeriesInput) { input.IntervalStarts = []time.Time{late} },
		"no intervals":        func(input *SumOrgModelUsageSeriesInput) { input.IntervalStarts = nil },
		"unordered intervals": func(input *SumOrgModelUsageSeriesInput) { input.IntervalStarts[1] = input.IntervalStarts[0] },
		"unknown group":       func(input *SumOrgModelUsageSeriesInput) { input.GroupBy = "agent" },
		"no group limit":      func(input *SumOrgModelUsageSeriesInput) { input.GroupLimit = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.IntervalStarts = append([]time.Time(nil), valid.IntervalStarts...)
			mutate(&input)
			if err := input.validate(); err == nil {
				t.Fatal("validate succeeded, want error")
			}
		})
	}
}
