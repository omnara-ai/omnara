package executionstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type UsageInterval string

const (
	UsageIntervalHour  UsageInterval = "hour"
	UsageIntervalDay   UsageInterval = "day"
	UsageIntervalWeek  UsageInterval = "week"
	UsageIntervalMonth UsageInterval = "month"
)

type ModelUsageSeriesFilter struct {
	OrgIDs                  []uuid.UUID
	ProjectIDs              []uuid.UUID
	AgentProfileIDs         []uuid.UUID
	IncludeProfileSubagents bool
	Since                   *time.Time
	Until                   time.Time
}

type ModelUsageSeries struct {
	BucketStarts []time.Time
	Rows         []ModelUsageSeriesRow
	Totals       ModelUsageTotals
	ActiveAgents int64
}

// ModelUsageSeriesRow is the usage of one configured model by agents of one
// profile within one bucket. ProfileID is uuid.Nil for agents without a profile.
type ModelUsageSeriesRow struct {
	Bucket      int
	ModelID     uuid.UUID
	ModelName   string
	ProfileID   uuid.UUID
	ProfileName string
	Totals      ModelUsageTotals
}

// UsageBucketStarts returns the start of every interval in location from the
// one containing since up to until. It reports false when there would be more
// than limit buckets.
func UsageBucketStarts(
	since, until time.Time,
	interval UsageInterval,
	location *time.Location,
	limit int,
) ([]time.Time, bool) {
	local := since.In(location)
	year, month, day := local.Date()
	switch interval {
	case UsageIntervalHour:
		return usageHourStarts(local, until, limit)
	case UsageIntervalDay:
	case UsageIntervalWeek:
		day -= (int(local.Weekday()) + 6) % 7
	case UsageIntervalMonth:
		day = 1
	}
	starts := []time.Time{}
	for {
		start := usageDayStart(year, month, day, location)
		if len(starts) > 0 && !start.Before(until) {
			return starts, true
		}
		if len(starts) == limit {
			return nil, false
		}
		starts = append(starts, start)
		switch interval {
		case UsageIntervalWeek:
			day += 7
		case UsageIntervalMonth:
			month++
		default:
			day++
		}
	}
}

// usageHourStarts steps whole elapsed hours from the local hour containing
// since, so repeated and skipped daylight-saving hours stay one hour wide.
func usageHourStarts(since, until time.Time, limit int) ([]time.Time, bool) {
	start := since.Add(-time.Duration(since.Minute())*time.Minute -
		time.Duration(since.Second())*time.Second -
		time.Duration(since.Nanosecond()))
	starts := []time.Time{}
	for len(starts) == 0 || start.Before(until) {
		if len(starts) == limit {
			return nil, false
		}
		starts = append(starts, start)
		start = start.Add(time.Hour)
	}
	return starts, true
}

func usageDayStart(year int, month time.Month, day int, location *time.Location) time.Time {
	year, month, day = time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Date()
	start := time.Date(year, month, day, 0, 0, 0, 0, location)
	if startYear, startMonth, startDay := start.Date(); startYear != year || startMonth != month || startDay != day {
		return time.Date(year, month, day, 1, 0, 0, 0, location)
	}
	return start
}

func (filter ModelUsageSeriesFilter) validate() error {
	// With no projects there is nothing to read, so a caller without any
	// memberships gets an empty series rather than an error.
	if len(filter.OrgIDs) == 0 && len(filter.ProjectIDs) > 0 {
		return errors.New("usage series orgs are required")
	}
	if filter.Until.IsZero() {
		return errors.New("usage series until is required")
	}
	if filter.Since != nil && !filter.Until.After(*filter.Since) {
		return errors.New("usage series until must be after since")
	}
	return nil
}

// FirstModelUsageAt returns when the earliest model call matching filter was
// made, or false when none match.
func (s *Store) FirstModelUsageAt(ctx context.Context, filter ModelUsageSeriesFilter) (time.Time, bool, error) {
	if err := filter.validate(); err != nil {
		return time.Time{}, false, err
	}
	if len(filter.ProjectIDs) == 0 {
		return time.Time{}, false, nil
	}
	first, err := s.q.FirstModelCallUsageAt(ctx, dbsqlc.FirstModelCallUsageAtParams{
		OrgIds:                  filter.OrgIDs,
		ProjectIds:              filter.ProjectIDs,
		Since:                   filter.Since,
		Until:                   filter.Until,
		AgentProfileIds:         filter.AgentProfileIDs,
		IncludeProfileSubagents: filter.IncludeProfileSubagents,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("first model usage: %w", err)
	}
	return first, true, nil
}

func (s *Store) SumModelUsageSeries(
	ctx context.Context,
	filter ModelUsageSeriesFilter,
	bucketStarts []time.Time,
) (ModelUsageSeries, error) {
	if err := filter.validate(); err != nil {
		return ModelUsageSeries{}, err
	}
	if len(bucketStarts) == 0 {
		return ModelUsageSeries{}, errors.New("usage series buckets are required")
	}
	if !slices.IsSortedFunc(bucketStarts, time.Time.Compare) || !bucketStarts[len(bucketStarts)-1].Before(filter.Until) {
		return ModelUsageSeries{}, errors.New("usage series buckets must ascend and start before until")
	}
	series := ModelUsageSeries{BucketStarts: bucketStarts, Rows: []ModelUsageSeriesRow{}}
	if len(filter.ProjectIDs) == 0 {
		totals, err := sumModelUsageTotals(nil)
		series.Totals = totals
		return series, err
	}
	rows, err := s.q.SumModelCallUsageByBucket(ctx, dbsqlc.SumModelCallUsageByBucketParams{
		BucketStarts:            bucketStarts,
		OrgIds:                  filter.OrgIDs,
		ProjectIds:              filter.ProjectIDs,
		Since:                   filter.Since,
		Until:                   filter.Until,
		AgentProfileIds:         filter.AgentProfileIDs,
		IncludeProfileSubagents: filter.IncludeProfileSubagents,
	})
	if err != nil {
		return ModelUsageSeries{}, fmt.Errorf("sum model usage by bucket: %w", err)
	}
	totals := make([]ModelUsageTotals, 0, len(rows))
	for _, row := range rows {
		bucket := int(row.BucketNumber) - 1
		if bucket < 0 || bucket >= len(bucketStarts) {
			return ModelUsageSeries{}, fmt.Errorf("sum model usage by bucket: bucket %d out of range", row.BucketNumber)
		}
		cost, ok := modelenvelope.ParseProviderReportedCostUSD(row.ProviderReportedCostUsd)
		if !ok {
			return ModelUsageSeries{}, fmt.Errorf(
				"sum model usage by bucket: invalid cost total %q", row.ProviderReportedCostUsd,
			)
		}
		rowTotals := ModelUsageTotals{
			ModelCalls:                 row.ModelCalls,
			ModelCallsWithReportedCost: row.ModelCallsWithReportedCost,
			InputTokensTotal:           row.InputTokensTotal,
			UncachedInputTokens:        row.UncachedInputTokens,
			CacheReadInputTokens:       row.CacheReadInputTokens,
			CacheWriteInputTokens:      row.CacheWriteInputTokens,
			OutputTokensTotal:          row.OutputTokensTotal,
			ReasoningOutputTokens:      row.ReasoningOutputTokens,
			ProviderReportedCostUSD:    cost,
		}
		totals = append(totals, rowTotals)
		series.Rows = append(series.Rows, ModelUsageSeriesRow{
			Bucket:      bucket,
			ModelID:     row.ConfiguredModelID,
			ModelName:   row.ConfiguredModelName,
			ProfileID:   storeutil.IDFromPtr(row.AgentProfileID),
			ProfileName: row.AgentProfileName,
			Totals:      rowTotals,
		})
	}
	series.Totals, err = sumModelUsageTotals(totals)
	if err != nil {
		return ModelUsageSeries{}, err
	}
	series.ActiveAgents, err = s.q.CountAgentsWithModelCalls(ctx, dbsqlc.CountAgentsWithModelCallsParams{
		OrgIds:                  filter.OrgIDs,
		ProjectIds:              filter.ProjectIDs,
		Since:                   filter.Since,
		Until:                   filter.Until,
		AgentProfileIds:         filter.AgentProfileIDs,
		IncludeProfileSubagents: filter.IncludeProfileSubagents,
	})
	if err != nil {
		return ModelUsageSeries{}, fmt.Errorf("count agents with model calls: %w", err)
	}
	return series, nil
}
