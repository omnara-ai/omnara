package executionstore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type SumOrgModelUsageSeriesInput struct {
	OrgID      uuid.UUID
	ProjectIDs []uuid.UUID
	DayStarts  []time.Time
	GroupLimit int
}

type ModelUsageSeries struct {
	Totals       ModelUsageTotals
	ActiveAgents int64
	Models       []ModelUsageSeriesGroup
	Profiles     []ModelUsageSeriesGroup
	Days         []ModelUsageSeriesDay
}

type ModelUsageSeriesGroup struct {
	ID     uuid.UUID
	Name   string
	Totals ModelUsageTotals
}

type ModelUsageSeriesDay struct {
	Start    time.Time
	Totals   ModelUsageTotals
	Models   []ModelUsageSeriesGroupTokens
	Profiles []ModelUsageSeriesGroupTokens
}

type ModelUsageSeriesGroupTokens struct {
	GroupID uuid.UUID
	Tokens  int64
}

type modelUsageSeriesRow struct {
	dayNumber   int32
	modelID     uuid.UUID
	modelName   string
	profileID   uuid.UUID
	profileName string
	totals      ModelUsageTotals
}

func UsageDayStarts(now time.Time, days int, location *time.Location) []time.Time {
	local := now.In(location)
	starts := make([]time.Time, days)
	for index := range starts {
		starts[index] = usageDayStart(local, index-days+1)
	}
	return starts
}

func usageDayStart(local time.Time, offset int) time.Time {
	year, month, day := time.Date(local.Year(), local.Month(), local.Day()+offset, 12, 0, 0, 0, time.UTC).Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, local.Location())
	if startYear, startMonth, startDay := start.Date(); startYear != year || startMonth != month || startDay != day {
		return time.Date(year, month, day, 1, 0, 0, 0, local.Location())
	}
	return start
}

func (input SumOrgModelUsageSeriesInput) validate() error {
	if input.OrgID == uuid.Nil {
		return errors.New("org is required")
	}
	if len(input.DayStarts) == 0 {
		return errors.New("usage series days are required")
	}
	for index := 1; index < len(input.DayStarts); index++ {
		if !input.DayStarts[index].After(input.DayStarts[index-1]) {
			return errors.New("usage series day starts must ascend")
		}
	}
	if input.GroupLimit <= 0 {
		return errors.New("usage series group limit must be positive")
	}
	return nil
}

func (s *Store) SumOrgModelUsageSeries(
	ctx context.Context,
	input SumOrgModelUsageSeriesInput,
) (ModelUsageSeries, error) {
	if err := input.validate(); err != nil {
		return ModelUsageSeries{}, err
	}
	if len(input.ProjectIDs) == 0 {
		return assembleModelUsageSeries(input.DayStarts, nil, input.GroupLimit)
	}
	rows, err := s.q.SumModelCallUsageByDay(ctx, dbsqlc.SumModelCallUsageByDayParams{
		DayStarts:  input.DayStarts,
		OrgID:      input.OrgID,
		ProjectIds: input.ProjectIDs,
		Since:      input.DayStarts[0],
	})
	if err != nil {
		return ModelUsageSeries{}, fmt.Errorf("sum model usage by day: %w", err)
	}
	seriesRows := make([]modelUsageSeriesRow, 0, len(rows))
	for _, row := range rows {
		cost, ok := modelenvelope.ParseProviderReportedCostUSD(row.ProviderReportedCostUsd)
		if !ok {
			return ModelUsageSeries{}, fmt.Errorf("sum model usage by day: invalid cost total %q", row.ProviderReportedCostUsd)
		}
		seriesRows = append(seriesRows, modelUsageSeriesRow{
			dayNumber:   row.DayNumber,
			modelID:     row.ConfiguredModelID,
			modelName:   row.ConfiguredModelName,
			profileID:   storeutil.IDFromPtr(row.AgentProfileID),
			profileName: row.AgentProfileName,
			totals: ModelUsageTotals{
				ModelCalls:                 row.ModelCalls,
				ModelCallsWithReportedCost: row.ModelCallsWithReportedCost,
				InputTokensTotal:           row.InputTokensTotal,
				UncachedInputTokens:        row.UncachedInputTokens,
				CacheReadInputTokens:       row.CacheReadInputTokens,
				CacheWriteInputTokens:      row.CacheWriteInputTokens,
				OutputTokensTotal:          row.OutputTokensTotal,
				ReasoningOutputTokens:      row.ReasoningOutputTokens,
				ProviderReportedCostUSD:    cost,
			},
		})
	}
	series, err := assembleModelUsageSeries(input.DayStarts, seriesRows, input.GroupLimit)
	if err != nil {
		return ModelUsageSeries{}, err
	}
	series.ActiveAgents, err = s.q.CountAgentsWithModelCalls(ctx, dbsqlc.CountAgentsWithModelCallsParams{
		OrgID:      input.OrgID,
		ProjectIds: input.ProjectIDs,
		Since:      input.DayStarts[0],
	})
	if err != nil {
		return ModelUsageSeries{}, fmt.Errorf("count agents with model calls: %w", err)
	}
	return series, nil
}

func assembleModelUsageSeries(
	dayStarts []time.Time,
	rows []modelUsageSeriesRow,
	groupLimit int,
) (ModelUsageSeries, error) {
	dayRows := make([][]ModelUsageTotals, len(dayStarts))
	for _, row := range rows {
		index := int(row.dayNumber) - 1
		if index < 0 || index >= len(dayStarts) {
			return ModelUsageSeries{}, fmt.Errorf("sum model usage by day: day %d out of range", row.dayNumber)
		}
		dayRows[index] = append(dayRows[index], row.totals)
	}
	series := ModelUsageSeries{Days: make([]ModelUsageSeriesDay, len(dayStarts))}
	dayTotals := make([]ModelUsageTotals, len(dayStarts))
	for index, start := range dayStarts {
		totals, err := sumModelUsageTotals(dayRows[index])
		if err != nil {
			return ModelUsageSeries{}, err
		}
		dayTotals[index] = totals
		series.Days[index] = ModelUsageSeriesDay{Start: start, Totals: totals}
	}
	totals, err := sumModelUsageTotals(dayTotals)
	if err != nil {
		return ModelUsageSeries{}, err
	}
	series.Totals = totals
	models, modelDays, err := usageBreakdown(rows, len(dayStarts), groupLimit, usageSeriesModel)
	if err != nil {
		return ModelUsageSeries{}, err
	}
	profiles, profileDays, err := usageBreakdown(rows, len(dayStarts), groupLimit, usageSeriesProfile)
	if err != nil {
		return ModelUsageSeries{}, err
	}
	series.Models, series.Profiles = models, profiles
	for index := range series.Days {
		series.Days[index].Models, series.Days[index].Profiles = modelDays[index], profileDays[index]
	}
	return series, nil
}

func usageSeriesModel(row modelUsageSeriesRow) (uuid.UUID, string) {
	return row.modelID, row.modelName
}

func usageSeriesProfile(row modelUsageSeriesRow) (uuid.UUID, string) {
	return row.profileID, row.profileName
}

func usageBreakdown(
	rows []modelUsageSeriesRow,
	days, groupLimit int,
	group func(modelUsageSeriesRow) (uuid.UUID, string),
) ([]ModelUsageSeriesGroup, [][]ModelUsageSeriesGroupTokens, error) {
	order := []uuid.UUID{}
	names := map[uuid.UUID]string{}
	groupRows := map[uuid.UUID][]ModelUsageTotals{}
	dayGroupTokens := make([]map[uuid.UUID]int64, days)
	for _, row := range rows {
		id, name := group(row)
		if _, seen := groupRows[id]; !seen {
			order = append(order, id)
			names[id] = name
		}
		groupRows[id] = append(groupRows[id], row.totals)
		index := int(row.dayNumber) - 1
		if dayGroupTokens[index] == nil {
			dayGroupTokens[index] = map[uuid.UUID]int64{}
		}
		dayGroupTokens[index][id] += row.totals.tokens()
	}
	groups := make([]ModelUsageSeriesGroup, 0, len(order))
	for _, id := range order {
		totals, err := sumModelUsageTotals(groupRows[id])
		if err != nil {
			return nil, nil, err
		}
		groups = append(groups, ModelUsageSeriesGroup{ID: id, Name: names[id], Totals: totals})
	}
	slices.SortStableFunc(groups, func(left, right ModelUsageSeriesGroup) int {
		return cmp.Or(
			cmp.Compare(right.Totals.tokens(), left.Totals.tokens()),
			cmp.Compare(left.Name, right.Name),
			cmp.Compare(left.ID.String(), right.ID.String()),
		)
	})
	groups = groups[:min(len(groups), groupLimit)]
	perDay := make([][]ModelUsageSeriesGroupTokens, days)
	for index := range perDay {
		perDay[index] = []ModelUsageSeriesGroupTokens{}
		for _, kept := range groups {
			tokens, ok := dayGroupTokens[index][kept.ID]
			if !ok {
				continue
			}
			perDay[index] = append(perDay[index], ModelUsageSeriesGroupTokens{GroupID: kept.ID, Tokens: tokens})
		}
	}
	return groups, perDay, nil
}

func (t ModelUsageTotals) tokens() int64 {
	return t.InputTokensTotal + t.OutputTokensTotal
}
