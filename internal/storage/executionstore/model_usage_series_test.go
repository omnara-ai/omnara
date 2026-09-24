package executionstore

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
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

func assertUsageDayStarts(t *testing.T, got []time.Time, want ...time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("day starts = %v, want %v", got, want)
	}
	for index := range want {
		if !got[index].Equal(want[index]) {
			t.Fatalf("day start %d = %v, want %v", index, got[index], want[index])
		}
	}
}

func TestUsageDayStartsEndTodayAndFollowDaylightSaving(t *testing.T) {
	losAngeles := loadUsageSeriesLocation(t, "America/Los_Angeles")
	assertUsageDayStarts(
		t, UsageDayStarts(time.Date(2026, 3, 9, 8, 0, 0, 0, losAngeles), 3, losAngeles),
		time.Date(2026, 3, 7, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 9, 7, 0, 0, 0, time.UTC),
	)
}

func TestUsageDayStartsBeginDaysWhenMidnightIsSkipped(t *testing.T) {
	cairo := loadUsageSeriesLocation(t, "Africa/Cairo")
	assertUsageDayStarts(
		t, UsageDayStarts(time.Date(2026, 4, 25, 6, 0, 0, 0, cairo), 3, cairo),
		time.Date(2026, 4, 22, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 23, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 24, 21, 0, 0, 0, time.UTC),
	)
}

func TestUsageDayStartsUseTheLocalDate(t *testing.T) {
	tokyo := loadUsageSeriesLocation(t, "Asia/Tokyo")
	assertUsageDayStarts(
		t, UsageDayStarts(time.Date(2026, 9, 22, 20, 0, 0, 0, time.UTC), 2, tokyo),
		time.Date(2026, 9, 21, 15, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC),
	)
}

func TestAssembleModelUsageSeriesRanksBothBreakdownsAndKeepsEmptyDays(t *testing.T) {
	starts := []time.Time{
		time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
	}
	light, heavy, tail := uuid.New(), uuid.New(), uuid.New()
	reviewer := uuid.New()
	usage := func(input, output int64, cost string) ModelUsageTotals {
		return ModelUsageTotals{
			ModelCalls: 1, ModelCallsWithReportedCost: 1, InputTokensTotal: input, UncachedInputTokens: input,
			OutputTokensTotal: output, ProviderReportedCostUSD: modelenvelopeCost(t, cost),
		}
	}
	series, err := assembleModelUsageSeries(starts, []modelUsageSeriesRow{
		{dayNumber: 1, modelID: light, modelName: "light", profileID: reviewer, profileName: "Reviewer",
			totals: usage(80, 20, "0.1")},
		{dayNumber: 1, modelID: heavy, modelName: "heavy", profileID: reviewer, profileName: "Reviewer",
			totals: usage(250, 50, "0.2")},
		{dayNumber: 3, modelID: light, modelName: "light", totals: usage(40, 10, "0.05")},
		{dayNumber: 3, modelID: tail, modelName: "tail", profileID: reviewer, profileName: "Reviewer",
			totals: usage(9, 1, "0.001")},
	}, 2)
	if err != nil {
		t.Fatalf("assemble series: %v", err)
	}
	if series.Totals.ModelCalls != 4 || series.Totals.tokens() != 460 || series.Totals.ProviderReportedCostUSD != "0.351" {
		t.Fatalf("totals = %+v", series.Totals)
	}
	if len(series.Models) != 2 || series.Models[0].ID != heavy || series.Models[1].ID != light ||
		series.Models[1].Totals.tokens() != 150 || series.Models[1].Name != "light" {
		t.Fatalf("models = %+v, want heavy then light", series.Models)
	}
	if len(series.Profiles) != 2 || series.Profiles[0].ID != reviewer || series.Profiles[0].Totals.tokens() != 410 ||
		series.Profiles[1].ID != uuid.Nil || series.Profiles[1].Name != "" || series.Profiles[1].Totals.tokens() != 50 {
		t.Fatalf("profiles = %+v, want Reviewer then no profile", series.Profiles)
	}
	if len(series.Days) != 3 {
		t.Fatalf("days = %+v, want 3", series.Days)
	}
	first := series.Days[0]
	if !first.Start.Equal(starts[0]) || first.Totals.tokens() != 400 || len(first.Models) != 2 ||
		first.Models[0].GroupID != heavy || first.Models[1].GroupID != light ||
		len(first.Profiles) != 1 || first.Profiles[0].Tokens != 400 {
		t.Fatalf("first day = %+v", first)
	}
	empty := series.Days[1]
	if empty.Totals.ModelCalls != 0 || empty.Totals.ProviderReportedCostUSD != "0" ||
		len(empty.Models) != 0 || len(empty.Profiles) != 0 {
		t.Fatalf("empty day = %+v", empty)
	}
	last := series.Days[2]
	if last.Totals.tokens() != 60 || last.Totals.ProviderReportedCostUSD != "0.051" || len(last.Models) != 1 ||
		last.Models[0].GroupID != light || len(last.Profiles) != 2 || last.Profiles[0].GroupID != reviewer ||
		last.Profiles[0].Tokens != 10 || last.Profiles[1].GroupID != uuid.Nil {
		t.Fatalf("last day = %+v, want light only with tail in totals", last)
	}
}

func TestAssembleModelUsageSeriesRejectsUnknownDays(t *testing.T) {
	starts := []time.Time{time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)}
	for _, number := range []int32{0, 2} {
		_, err := assembleModelUsageSeries(starts, []modelUsageSeriesRow{{
			dayNumber: number, modelID: uuid.New(), modelName: "model",
			totals: ModelUsageTotals{ModelCalls: 1, ProviderReportedCostUSD: "0"},
		}}, 5)
		if err == nil {
			t.Fatalf("day %d assembled, want error", number)
		}
	}
}

func TestSumOrgModelUsageSeriesInputValidation(t *testing.T) {
	today := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	valid := SumOrgModelUsageSeriesInput{
		OrgID:      uuid.New(),
		DayStarts:  []time.Time{today.AddDate(0, 0, -1), today},
		GroupLimit: 8,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	for name, mutate := range map[string]func(*SumOrgModelUsageSeriesInput){
		"missing org":    func(input *SumOrgModelUsageSeriesInput) { input.OrgID = uuid.Nil },
		"no days":        func(input *SumOrgModelUsageSeriesInput) { input.DayStarts = nil },
		"unordered days": func(input *SumOrgModelUsageSeriesInput) { input.DayStarts[1] = input.DayStarts[0] },
		"no group limit": func(input *SumOrgModelUsageSeriesInput) { input.GroupLimit = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.DayStarts = append([]time.Time(nil), valid.DayStarts...)
			mutate(&input)
			if err := input.validate(); err == nil {
				t.Fatal("validate succeeded, want error")
			}
		})
	}
}
