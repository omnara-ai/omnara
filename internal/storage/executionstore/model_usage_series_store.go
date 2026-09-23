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
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxUsageIntervals = 366

type UsageInterval string

const (
	UsageIntervalHour UsageInterval = "hour"
	UsageIntervalDay  UsageInterval = "day"
)

type UsageGroupBy string

const (
	UsageGroupByModel   UsageGroupBy = "model"
	UsageGroupByProject UsageGroupBy = "project"
	UsageGroupByProfile UsageGroupBy = "profile"
)

type SumOrgModelUsageSeriesInput struct {
	OrgID          uuid.UUID
	ProjectIDs     []uuid.UUID
	Window         UsageWindow
	IntervalStarts []time.Time
	GroupBy        UsageGroupBy
	GroupLimit     int
}

type ModelUsageSeries struct {
	Totals       ModelUsageTotals
	ActiveAgents int64
	Groups       []ModelUsageSeriesGroup
	Intervals    []ModelUsageSeriesInterval
}

type ModelUsageSeriesGroup struct {
	ID     uuid.UUID
	Name   string
	Totals ModelUsageTotals
}

type ModelUsageSeriesInterval struct {
	Start  time.Time
	Totals ModelUsageTotals
	Groups []ModelUsageSeriesGroupTotals
}

type ModelUsageSeriesGroupTotals struct {
	GroupID uuid.UUID
	Totals  ModelUsageTotals
}

type modelUsageSeriesRow struct {
	intervalNumber int32
	groupID        uuid.UUID
	groupName      string
	totals         ModelUsageTotals
}

func UsageIntervalStarts(
	since, until time.Time,
	interval UsageInterval,
	location *time.Location,
) ([]time.Time, error) {
	if location == nil {
		return nil, errors.New("usage interval location is required")
	}
	if interval != UsageIntervalHour && interval != UsageIntervalDay {
		return nil, fmt.Errorf("unsupported usage interval %q", interval)
	}
	if !until.After(since) {
		return nil, storeerr.InvalidRequest(errors.New("until must be after since"))
	}
	local := since.In(location)
	starts := make([]time.Time, 0, 32)
	for offset := 0; ; offset++ {
		start := usageIntervalStart(local, interval, offset)
		if !start.Before(until) {
			return starts, nil
		}
		if len(starts) == MaxUsageIntervals {
			return nil, storeerr.InvalidRequest(fmt.Errorf(
				"the window spans more than %d intervals; shorten it or use a longer interval", MaxUsageIntervals,
			))
		}
		starts = append(starts, start)
	}
}

func usageIntervalStart(since time.Time, interval UsageInterval, offset int) time.Time {
	if interval == UsageIntervalHour {
		elapsed := time.Duration(since.Minute())*time.Minute +
			time.Duration(since.Second())*time.Second +
			time.Duration(since.Nanosecond())
		return since.Add(-elapsed).Add(time.Duration(offset) * time.Hour)
	}
	year, month, day := time.Date(since.Year(), since.Month(), since.Day()+offset, 12, 0, 0, 0, time.UTC).Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, since.Location())
	if startYear, startMonth, startDay := start.Date(); startYear != year || startMonth != month || startDay != day {
		return time.Date(year, month, day, 1, 0, 0, 0, since.Location())
	}
	return start
}

func (input SumOrgModelUsageSeriesInput) validate() error {
	if input.OrgID == uuid.Nil {
		return errors.New("org is required")
	}
	if input.Window.Since == nil {
		return errors.New("usage series since is required")
	}
	if err := input.Window.validate(); err != nil {
		return err
	}
	if len(input.IntervalStarts) == 0 || input.IntervalStarts[0].After(*input.Window.Since) {
		return errors.New("usage series intervals must start at or before since")
	}
	for index := 1; index < len(input.IntervalStarts); index++ {
		if !input.IntervalStarts[index].After(input.IntervalStarts[index-1]) {
			return errors.New("usage series interval starts must ascend")
		}
	}
	if !slices.Contains([]UsageGroupBy{UsageGroupByModel, UsageGroupByProject, UsageGroupByProfile}, input.GroupBy) {
		return fmt.Errorf("unsupported usage group %q", input.GroupBy)
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
		return assembleModelUsageSeries(input.IntervalStarts, nil, input.GroupLimit)
	}
	rows, err := s.listModelUsageSeriesRows(ctx, input)
	if err != nil {
		return ModelUsageSeries{}, err
	}
	series, err := assembleModelUsageSeries(input.IntervalStarts, rows, input.GroupLimit)
	if err != nil {
		return ModelUsageSeries{}, err
	}
	series.ActiveAgents, err = s.q.CountAgentsWithModelCalls(ctx, dbsqlc.CountAgentsWithModelCallsParams{
		OrgID:      input.OrgID,
		ProjectIds: input.ProjectIDs,
		Since:      *input.Window.Since,
		Until:      input.Window.Until,
	})
	if err != nil {
		return ModelUsageSeries{}, fmt.Errorf("count agents with model calls: %w", err)
	}
	return series, nil
}

func (s *Store) listModelUsageSeriesRows(
	ctx context.Context,
	input SumOrgModelUsageSeriesInput,
) ([]modelUsageSeriesRow, error) {
	rows, err := s.queryModelUsageSeriesRows(ctx, input)
	if err != nil {
		return nil, err
	}
	out := make([]modelUsageSeriesRow, 0, len(rows))
	for _, row := range rows {
		cost, ok := modelenvelope.ParseProviderReportedCostUSD(row.ProviderReportedCostUsd)
		if !ok {
			return nil, fmt.Errorf("sum model usage series: invalid cost total %q", row.ProviderReportedCostUsd)
		}
		out = append(out, modelUsageSeriesRow{
			intervalNumber: row.IntervalNumber,
			groupID:        row.GroupID,
			groupName:      row.GroupName,
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
	return out, nil
}

func (s *Store) queryModelUsageSeriesRows(
	ctx context.Context,
	input SumOrgModelUsageSeriesInput,
) ([]dbsqlc.SumModelCallUsageByIntervalAndModelRow, error) {
	params := dbsqlc.SumModelCallUsageByIntervalAndModelParams{
		IntervalStarts: input.IntervalStarts,
		OrgID:          input.OrgID,
		ProjectIds:     input.ProjectIDs,
		Since:          *input.Window.Since,
		Until:          input.Window.Until,
	}
	switch input.GroupBy {
	case UsageGroupByModel:
		rows, err := s.q.SumModelCallUsageByIntervalAndModel(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("sum model usage series by model: %w", err)
		}
		return rows, nil
	case UsageGroupByProject:
		projectRows, err := s.q.SumModelCallUsageByIntervalAndProject(
			ctx, dbsqlc.SumModelCallUsageByIntervalAndProjectParams(params),
		)
		if err != nil {
			return nil, fmt.Errorf("sum model usage series by project: %w", err)
		}
		rows := make([]dbsqlc.SumModelCallUsageByIntervalAndModelRow, 0, len(projectRows))
		for _, row := range projectRows {
			rows = append(rows, dbsqlc.SumModelCallUsageByIntervalAndModelRow(row))
		}
		return rows, nil
	case UsageGroupByProfile:
		profileRows, err := s.q.SumModelCallUsageByIntervalAndProfile(
			ctx, dbsqlc.SumModelCallUsageByIntervalAndProfileParams(params),
		)
		if err != nil {
			return nil, fmt.Errorf("sum model usage series by profile: %w", err)
		}
		rows := make([]dbsqlc.SumModelCallUsageByIntervalAndModelRow, 0, len(profileRows))
		for _, row := range profileRows {
			rows = append(rows, dbsqlc.SumModelCallUsageByIntervalAndModelRow{
				IntervalNumber:             row.IntervalNumber,
				GroupID:                    storeutil.IDFromPtr(row.GroupID),
				GroupName:                  row.GroupName,
				ModelCalls:                 row.ModelCalls,
				ModelCallsWithReportedCost: row.ModelCallsWithReportedCost,
				InputTokensTotal:           row.InputTokensTotal,
				UncachedInputTokens:        row.UncachedInputTokens,
				CacheReadInputTokens:       row.CacheReadInputTokens,
				CacheWriteInputTokens:      row.CacheWriteInputTokens,
				OutputTokensTotal:          row.OutputTokensTotal,
				ReasoningOutputTokens:      row.ReasoningOutputTokens,
				ProviderReportedCostUsd:    row.ProviderReportedCostUsd,
			})
		}
		return rows, nil
	}
	return nil, fmt.Errorf("unsupported usage group %q", input.GroupBy)
}

func assembleModelUsageSeries(
	intervalStarts []time.Time,
	rows []modelUsageSeriesRow,
	groupLimit int,
) (ModelUsageSeries, error) {
	zero := ModelUsageTotals{ProviderReportedCostUSD: "0"}
	series := ModelUsageSeries{
		Totals:    zero,
		Groups:    []ModelUsageSeriesGroup{},
		Intervals: make([]ModelUsageSeriesInterval, len(intervalStarts)),
	}
	for index, start := range intervalStarts {
		series.Intervals[index] = ModelUsageSeriesInterval{
			Start: start, Totals: zero, Groups: []ModelUsageSeriesGroupTotals{},
		}
	}
	groups := []ModelUsageSeriesGroup{}
	groupIndex := map[uuid.UUID]int{}
	intervalGroups := make([]map[uuid.UUID]ModelUsageTotals, len(intervalStarts))
	for _, row := range rows {
		index := int(row.intervalNumber) - 1
		if index < 0 || index >= len(intervalStarts) {
			return ModelUsageSeries{}, fmt.Errorf("sum model usage series: interval %d out of range", row.intervalNumber)
		}
		totals, err := series.Intervals[index].Totals.plus(row.totals)
		if err != nil {
			return ModelUsageSeries{}, err
		}
		series.Intervals[index].Totals = totals
		if series.Totals, err = series.Totals.plus(row.totals); err != nil {
			return ModelUsageSeries{}, err
		}
		position, seen := groupIndex[row.groupID]
		if !seen {
			position = len(groups)
			groupIndex[row.groupID] = position
			groups = append(groups, ModelUsageSeriesGroup{ID: row.groupID, Name: row.groupName, Totals: zero})
		}
		if groups[position].Totals, err = groups[position].Totals.plus(row.totals); err != nil {
			return ModelUsageSeries{}, err
		}
		if intervalGroups[index] == nil {
			intervalGroups[index] = map[uuid.UUID]ModelUsageTotals{}
		}
		if intervalGroups[index][row.groupID], err = intervalGroups[index][row.groupID].plus(row.totals); err != nil {
			return ModelUsageSeries{}, err
		}
	}
	slices.SortStableFunc(groups, func(left, right ModelUsageSeriesGroup) int {
		return cmp.Or(
			cmp.Compare(right.Totals.tokens(), left.Totals.tokens()),
			cmp.Compare(left.Name, right.Name),
			cmp.Compare(left.ID.String(), right.ID.String()),
		)
	})
	if len(groups) > groupLimit {
		groups = groups[:groupLimit]
	}
	series.Groups = groups
	for index := range series.Intervals {
		for _, group := range groups {
			totals, ok := intervalGroups[index][group.ID]
			if !ok {
				continue
			}
			series.Intervals[index].Groups = append(
				series.Intervals[index].Groups,
				ModelUsageSeriesGroupTotals{GroupID: group.ID, Totals: totals},
			)
		}
	}
	return series, nil
}

func (t ModelUsageTotals) tokens() int64 {
	return t.InputTokensTotal + t.OutputTokensTotal
}

func (t ModelUsageTotals) plus(other ModelUsageTotals) (ModelUsageTotals, error) {
	costs := make([]string, 0, 2)
	for _, cost := range []modelenvelope.ProviderReportedCostUSD{
		t.ProviderReportedCostUSD, other.ProviderReportedCostUSD,
	} {
		if cost != "" {
			costs = append(costs, string(cost))
		}
	}
	cost := modelenvelope.ProviderReportedCostUSD("0")
	if len(costs) > 0 {
		sum, ok := modelenvelope.SumProviderReportedCostUSD(costs...)
		if !ok {
			return ModelUsageTotals{}, errors.New("sum model usage series: invalid cost totals")
		}
		cost = sum
	}
	return ModelUsageTotals{
		ModelCalls:                 t.ModelCalls + other.ModelCalls,
		ModelCallsWithReportedCost: t.ModelCallsWithReportedCost + other.ModelCallsWithReportedCost,
		InputTokensTotal:           t.InputTokensTotal + other.InputTokensTotal,
		UncachedInputTokens:        t.UncachedInputTokens + other.UncachedInputTokens,
		CacheReadInputTokens:       t.CacheReadInputTokens + other.CacheReadInputTokens,
		CacheWriteInputTokens:      t.CacheWriteInputTokens + other.CacheWriteInputTokens,
		OutputTokensTotal:          t.OutputTokensTotal + other.OutputTokensTotal,
		ReasoningOutputTokens:      t.ReasoningOutputTokens + other.ReasoningOutputTokens,
		ProviderReportedCostUSD:    cost,
	}, nil
}
