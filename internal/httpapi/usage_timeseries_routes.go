package httpapi

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const (
	usageTimeseriesMaxBuckets        = 400
	usageTimeseriesDefaultGroupLimit = 8
	usageTimeseriesDailyMaxSpan      = 92 * 24 * time.Hour
	usageTimeseriesWeeklyMaxSpan     = 730 * 24 * time.Hour
)

func (s strictOpenAPIServer) GetOrgUsageTimeseries(
	ctx context.Context,
	request openapi.GetOrgUsageTimeseriesRequestObject,
) (openapi.GetOrgUsageTimeseriesResponseObject, error) {
	params := request.Params
	// Default until before validating, so a since in the future is a bad request
	// rather than a window the store rejects.
	until := time.Now()
	if params.Until != nil {
		until = *params.Until
	}
	if _, err := usageWindowFromParams(params.Since, &until); err != nil {
		return nil, err
	}
	location, err := timezoneLocation(params.Timezone)
	if err != nil {
		return nil, err
	}
	metric := cmp.Or(valueOrZero(params.Metric), openapi.UsageTimeseriesMetricSumTokens)
	if !metric.Valid() {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid metric")
	}
	if params.GroupBy != nil && !params.GroupBy.Valid() {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid group_by")
	}
	if params.Interval != nil && !params.Interval.Valid() {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid interval")
	}
	groupLimit := cmp.Or(valueOrZero(params.GroupLimit), usageTimeseriesDefaultGroupLimit)
	projectIDs, err := usagePublicIDsFromParams(publicid.KindProject, "project_ids", params.ProjectIds)
	if err != nil {
		return nil, err
	}
	profileIDs, err := usagePublicIDsFromParams(publicid.KindAgentProfile, "agent_profile_ids", params.AgentProfileIds)
	if err != nil {
		return nil, err
	}
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	projectIDs, err = s.usageTimeseriesProjects(ctx, projectIDs)
	if err != nil {
		return nil, err
	}
	filter := executionstore.ModelUsageSeriesFilter{
		OrgIDs:                  []uuid.UUID{org.ID},
		ProjectIDs:              projectIDs,
		AgentProfileIDs:         profileIDs,
		IncludeProfileSubagents: profileIDs != nil && valueOrZero(params.IncludeSubagents),
		Since:                   params.Since,
		Until:                   until,
	}
	start, err := s.usageTimeseriesStart(ctx, filter)
	if err != nil {
		return nil, err
	}
	interval := usageTimeseriesInterval(params.Interval, until.Sub(start))
	bucketStarts, ok := executionstore.UsageBucketStarts(
		start, until, executionstore.UsageInterval(interval), location, usageTimeseriesMaxBuckets,
	)
	if !ok {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			fmt.Sprintf("window spans more than %d %s buckets", usageTimeseriesMaxBuckets, interval),
		)
	}
	series, err := s.server.store.Execution().SumModelUsageSeries(ctx, filter, bucketStarts)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := usageTimeseriesResponse(series, metric, params.GroupBy, groupLimit)
	if err != nil {
		return nil, err
	}
	response.Interval = interval
	response.Timezone = location.String()
	response.Until = until.UTC()
	return openapi.GetOrgUsageTimeseries200JSONResponse(response), nil
}

// usageTimeseriesProjects returns the projects the caller can read in the
// scoped org, narrowed to projectIDs when given; an unreadable one is not found.
func (s strictOpenAPIServer) usageTimeseriesProjects(
	ctx context.Context,
	projectIDs []uuid.UUID,
) ([]uuid.UUID, error) {
	scope, err := s.orgListScope(ctx, identitystore.ProjectActionRead)
	if err != nil {
		return nil, err
	}
	if projectIDs == nil {
		return scope.projectIDs, nil
	}
	for _, id := range projectIDs {
		if !slices.Contains(scope.projectIDs, id) {
			return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
	}
	return projectIDs, nil
}

// usageTimeseriesStart returns where the window begins: since when given,
// otherwise the earliest matching usage, or just before until when there is none.
func (s strictOpenAPIServer) usageTimeseriesStart(
	ctx context.Context,
	filter executionstore.ModelUsageSeriesFilter,
) (time.Time, error) {
	if filter.Since != nil {
		return *filter.Since, nil
	}
	first, ok, err := s.server.store.Execution().FirstModelUsageAt(ctx, filter)
	if err != nil {
		return time.Time{}, apierror.OrgScoped(err)
	}
	if !ok {
		return filter.Until.Add(-time.Nanosecond), nil
	}
	return first, nil
}

func usageTimeseriesInterval(
	requested *openapi.UsageTimeseriesInterval,
	span time.Duration,
) openapi.UsageTimeseriesInterval {
	switch {
	case requested != nil:
		return *requested
	case span <= usageTimeseriesDailyMaxSpan:
		return openapi.UsageTimeseriesIntervalDay
	case span <= usageTimeseriesWeeklyMaxSpan:
		return openapi.UsageTimeseriesIntervalWeek
	default:
		return openapi.UsageTimeseriesIntervalMonth
	}
}

func usagePublicIDsFromParams(kind publicid.Kind, name string, values *[]string) ([]uuid.UUID, error) {
	if values == nil {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(*values))
	for _, value := range *values {
		id, ok := parseOpenAPIPublicID(kind, value)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid "+name)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type usageTimeseriesGroupKey struct {
	kind openapi.UsageTimeseriesSeriesKind
	id   uuid.UUID
}

type usageTimeseriesGroup struct {
	key    usageTimeseriesGroupKey
	name   string
	total  float64
	values []float64
}

func usageTimeseriesResponse(
	series executionstore.ModelUsageSeries,
	metric openapi.UsageTimeseriesMetric,
	groupBy *openapi.UsageTimeseriesGroupBy,
	groupLimit int,
) (openapi.UsageTimeseries, error) {
	buckets := len(series.BucketStarts)
	groups := map[usageTimeseriesGroupKey]*usageTimeseriesGroup{}
	order := []*usageTimeseriesGroup{}
	for _, row := range series.Rows {
		value, err := usageMetricValue(metric, row.Totals)
		if err != nil {
			return openapi.UsageTimeseries{}, err
		}
		key, name := usageTimeseriesRowGroup(row, groupBy)
		group, ok := groups[key]
		if !ok {
			group = &usageTimeseriesGroup{key: key, name: name, values: make([]float64, buckets)}
			groups[key] = group
			order = append(order, group)
		}
		group.total += value
		group.values[row.Bucket] += value
	}
	order = slices.DeleteFunc(order, func(group *usageTimeseriesGroup) bool { return group.total <= 0 })
	slices.SortStableFunc(order, func(left, right *usageTimeseriesGroup) int {
		return cmp.Or(
			cmp.Compare(right.total, left.total),
			cmp.Compare(left.name, right.name),
			cmp.Compare(left.key.id.String(), right.key.id.String()),
		)
	})
	if groupBy == nil && len(order) == 0 {
		order = append(order, &usageTimeseriesGroup{
			key: usageTimeseriesGroupKey{kind: openapi.UsageTimeseriesSeriesKindAll}, values: make([]float64, buckets),
		})
	}
	if groupBy != nil && len(order) > groupLimit {
		other := &usageTimeseriesGroup{
			key: usageTimeseriesGroupKey{kind: openapi.UsageTimeseriesSeriesKindOther}, values: make([]float64, buckets),
		}
		for _, group := range order[groupLimit:] {
			other.total += group.total
			for index, value := range group.values {
				other.values[index] += value
			}
		}
		order = append(order[:groupLimit], other)
	}
	responseSeries := make([]openapi.UsageTimeseriesSeries, 0, len(order))
	for _, group := range order {
		item := openapi.UsageTimeseriesSeries{Kind: group.key.kind, Total: group.total, Values: group.values}
		if kind, ok := usageTimeseriesPublicIDKind(group.key.kind); ok {
			id, err := publicID(kind, group.key.id)
			if err != nil {
				return openapi.UsageTimeseries{}, err
			}
			item.Id, item.Name = new(id), new(group.name)
		}
		responseSeries = append(responseSeries, item)
	}
	bucketStarts := make([]openapi.Timestamp, 0, buckets)
	for _, start := range series.BucketStarts {
		bucketStarts = append(bucketStarts, start.UTC())
	}
	return openapi.UsageTimeseries{
		Metric:       metric,
		GroupBy:      groupBy,
		BucketStarts: bucketStarts,
		Totals:       usageTotalsResponse(series.Totals),
		ActiveAgents: series.ActiveAgents,
		Series:       responseSeries,
	}, nil
}

func usageTimeseriesRowGroup(
	row executionstore.ModelUsageSeriesRow,
	groupBy *openapi.UsageTimeseriesGroupBy,
) (usageTimeseriesGroupKey, string) {
	switch {
	case groupBy == nil:
		return usageTimeseriesGroupKey{kind: openapi.UsageTimeseriesSeriesKindAll}, ""
	case *groupBy == openapi.UsageTimeseriesGroupByModel:
		return usageTimeseriesGroupKey{kind: openapi.UsageTimeseriesSeriesKindModel, id: row.ModelID}, row.ModelName
	case row.ProfileID == uuid.Nil:
		return usageTimeseriesGroupKey{kind: openapi.UsageTimeseriesSeriesKindNoProfile}, ""
	default:
		return usageTimeseriesGroupKey{kind: openapi.UsageTimeseriesSeriesKindProfile, id: row.ProfileID}, row.ProfileName
	}
}

func usageTimeseriesPublicIDKind(kind openapi.UsageTimeseriesSeriesKind) (publicid.Kind, bool) {
	switch kind {
	case openapi.UsageTimeseriesSeriesKindModel:
		return publicid.KindConfiguredModel, true
	case openapi.UsageTimeseriesSeriesKindProfile:
		return publicid.KindAgentProfile, true
	default:
		return "", false
	}
}

func usageMetricValue(metric openapi.UsageTimeseriesMetric, totals executionstore.ModelUsageTotals) (float64, error) {
	switch metric {
	case openapi.UsageTimeseriesMetricSumTokens:
		return float64(totals.InputTokensTotal + totals.OutputTokensTotal), nil
	case openapi.UsageTimeseriesMetricSumInputTokens:
		return float64(totals.InputTokensTotal), nil
	case openapi.UsageTimeseriesMetricSumOutputTokens:
		return float64(totals.OutputTokensTotal), nil
	case openapi.UsageTimeseriesMetricCountModelCalls:
		return float64(totals.ModelCalls), nil
	case openapi.UsageTimeseriesMetricSumCost:
		cost, err := strconv.ParseFloat(string(totals.ProviderReportedCostUSD), 64)
		if err != nil {
			return 0, fmt.Errorf("parse usage cost %q: %w", totals.ProviderReportedCostUSD, err)
		}
		return cost, nil
	default:
		return 0, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid metric")
	}
}

func timezoneLocation(timezone *string) (*time.Location, error) {
	if timezone == nil {
		return time.UTC, nil
	}
	location, err := cronschedule.LoadLocation(*timezone)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid timezone")
	}
	return location, nil
}
