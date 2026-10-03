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
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const (
	usageTimeseriesMaxBuckets        = 400
	usageTimeseriesDefaultGroupLimit = 8
	usageTimeseriesDailyMaxSpan      = 92 * 24 * time.Hour
	usageTimeseriesWeeklyMaxSpan     = 730 * 24 * time.Hour
)

func (s strictOpenAPIServer) GetUsageTimeseries(
	ctx context.Context,
	request openapi.GetUsageTimeseriesRequestObject,
) (openapi.GetUsageTimeseriesResponseObject, error) {
	params := request.Params
	if _, err := usageWindowFromParams(params.Since, params.Until); err != nil {
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
	orgIDs, err := usagePublicIDsFromParams(publicid.KindOrganization, "org_ids", params.OrgIds)
	if err != nil {
		return nil, err
	}
	projectIDs, err := usagePublicIDsFromParams(publicid.KindProject, "project_ids", params.ProjectIds)
	if err != nil {
		return nil, err
	}
	profileIDs, err := usagePublicIDsFromParams(publicid.KindAgentProfile, "agent_profile_ids", params.AgentProfileIds)
	if err != nil {
		return nil, err
	}
	orgIDs, projectIDs, err = s.usageTimeseriesScope(ctx, orgIDs, projectIDs)
	if err != nil {
		return nil, err
	}
	until := time.Now()
	if params.Until != nil {
		until = *params.Until
	}
	filter := executionstore.ModelUsageSeriesFilter{
		OrgIDs:                  orgIDs,
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
	return openapi.GetUsageTimeseries200JSONResponse(response), nil
}

// usageTimeseriesScope resolves the orgs the caller belongs to and the projects
// it can read in them, narrowed to orgIDs and projectIDs when given.
func (s strictOpenAPIServer) usageTimeseriesScope(
	ctx context.Context,
	orgIDs, projectIDs []uuid.UUID,
) ([]uuid.UUID, []uuid.UUID, error) {
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		return nil, nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	memberships, err := s.server.store.Identity().ListOrgMembershipsForPrincipal(ctx, principal)
	if err != nil {
		return nil, nil, apierror.FromError(err)
	}
	memberOrgIDs := make([]uuid.UUID, 0, len(memberships))
	for _, membership := range memberships {
		memberOrgIDs = append(memberOrgIDs, membership.OrgID)
	}
	if orgIDs == nil {
		orgIDs = memberOrgIDs
	}
	readable := map[uuid.UUID]bool{}
	for _, orgID := range orgIDs {
		if !slices.Contains(memberOrgIDs, orgID) {
			return nil, nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		after := listing.KeysetCursor{}
		for {
			page, err := s.server.store.Identity().ListVisibleProjectsForPrincipal(
				ctx,
				identitystore.ListVisibleProjectsForPrincipalInput{
					OrgID: orgID, Principal: principal, Limit: orgListProjectPageSize, After: after,
				},
			)
			if err != nil {
				return nil, nil, apierror.OrgScoped(err)
			}
			for _, record := range page.Projects {
				if identitystore.ProjectRolesAllow(record.Roles, identitystore.ProjectActionRead) {
					readable[record.Project.ID] = true
				}
			}
			if !page.HasMore || len(page.Projects) == 0 {
				break
			}
			last := page.Projects[len(page.Projects)-1].Project
			after = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
		}
	}
	if projectIDs == nil {
		for id := range readable {
			projectIDs = append(projectIDs, id)
		}
		return orgIDs, projectIDs, nil
	}
	for _, id := range projectIDs {
		if !readable[id] {
			return nil, nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
	}
	return orgIDs, projectIDs, nil
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
