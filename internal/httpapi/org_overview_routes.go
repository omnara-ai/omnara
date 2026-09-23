package httpapi

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const (
	orgOverviewRecentLimit = 5
	// orgOverviewMaxProjects caps how many visible projects the overview
	// considers (and returns); recents beyond this cap are best-effort omitted.
	orgOverviewMaxProjects = 200

	orgOverviewUsageDefaultGroupLimit = 5
	orgOverviewPageSize               = 200
)

func (s strictOpenAPIServer) GetOrgOverview(
	ctx context.Context,
	_ openapi.GetOrgOverviewRequestObject,
) (openapi.GetOrgOverviewResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, _ := principalFromContext(ctx)
	page, err := s.server.store.Identity().ListVisibleProjectsForPrincipal(
		ctx,
		identitystore.ListVisibleProjectsForPrincipalInput{
			OrgID: org.ID, Principal: principal, Limit: orgOverviewMaxProjects,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	visible := page.Projects
	projects := make([]openapi.VisibleProject, 0, len(visible))
	agentProjectIDs := make([]uuid.UUID, 0, len(visible))
	profileProjectIDs := make([]uuid.UUID, 0, len(visible))
	for _, record := range visible {
		response, err := visibleProjectResponse(record)
		if err != nil {
			return nil, err
		}
		projects = append(projects, response)
		if identitystore.ProjectRolesAllow(record.Roles, identitystore.AgentActionRead) {
			agentProjectIDs = append(agentProjectIDs, record.Project.ID)
		}
		if identitystore.ProjectRolesAllow(record.Roles, identitystore.ProjectActionRead) {
			profileProjectIDs = append(profileProjectIDs, record.Project.ID)
		}
	}
	agentRecords, err := s.server.store.Execution().ListRecentAgentsForProjects(
		ctx,
		executionstore.ListRecentAgentsForProjectsInput{
			ProjectIDs: agentProjectIDs, Limit: orgOverviewRecentLimit,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	recentAgents := make([]openapi.Agent, 0, len(agentRecords))
	for _, record := range agentRecords {
		response, err := publicAgentResponseFromRecord(record)
		if err != nil {
			return nil, err
		}
		recentAgents = append(recentAgents, response)
	}
	profileRecords, err := s.server.store.Execution().ListRecentAgentProfilesForProjects(
		ctx,
		executionstore.ListRecentAgentProfilesForProjectsInput{
			ProjectIDs: profileProjectIDs, Limit: orgOverviewRecentLimit,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	recentProfiles := make([]openapi.AgentProfileSummary, 0, len(profileRecords))
	for _, record := range profileRecords {
		response, err := s.server.agentProfileSummaryFromRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		recentProfiles = append(recentProfiles, response)
	}
	referencedProfiles, err := s.referencedAgentProfiles(
		ctx, slices.Concat(profileProjectIDs, agentProjectIDs), agentRecords, profileRecords,
	)
	if err != nil {
		return nil, err
	}
	return openapi.GetOrgOverview200JSONResponse(openapi.OrgOverviewResponse{
		Projects:                projects,
		RecentAgents:            recentAgents,
		RecentAgentProfiles:     recentProfiles,
		ReferencedAgentProfiles: referencedProfiles,
	}), nil
}

func (s strictOpenAPIServer) referencedAgentProfiles(
	ctx context.Context,
	projectIDs []uuid.UUID,
	agents []executionstore.AgentRecord,
	profiles []executionstore.AgentProfileRecord,
) ([]openapi.OrgOverviewAgentProfileReference, error) {
	seen := map[uuid.UUID]bool{}
	profileIDs := make([]uuid.UUID, 0, len(agents)+len(profiles))
	add := func(id uuid.UUID) {
		if id != uuid.Nil && !seen[id] {
			seen[id] = true
			profileIDs = append(profileIDs, id)
		}
	}
	for _, profile := range profiles {
		add(profile.ID)
	}
	for _, agent := range agents {
		add(agent.AgentProfileID)
	}
	records, err := s.server.store.Execution().ListAgentProfilesWithAgentCounts(
		ctx,
		executionstore.ListAgentProfilesWithAgentCountsInput{ProjectIDs: projectIDs, ProfileIDs: profileIDs},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	references := make([]openapi.OrgOverviewAgentProfileReference, 0, len(records))
	for _, record := range records {
		profileID, err := publicID(publicid.KindAgentProfile, record.ID)
		if err != nil {
			return nil, err
		}
		references = append(references, openapi.OrgOverviewAgentProfileReference{
			Id: profileID, Name: record.Name, AgentCount: record.AgentCount,
		})
	}
	return references, nil
}

func (s strictOpenAPIServer) GetOrgOverviewUsage(
	ctx context.Context,
	request openapi.GetOrgOverviewUsageRequestObject,
) (openapi.GetOrgOverviewUsageResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	params := request.Params
	window, err := usageWindowFromParams(&params.Since, params.Until)
	if err != nil {
		return nil, err
	}
	end := time.Now()
	if params.Until != nil {
		end = *params.Until
	}
	location, err := orgOverviewUsageLocation(params.Timezone)
	if err != nil {
		return nil, err
	}
	interval := executionstore.UsageIntervalDay
	if params.Interval != nil && *params.Interval == openapi.Hour {
		interval = executionstore.UsageIntervalHour
	}
	intervalStarts, err := executionstore.UsageIntervalStarts(params.Since, end, interval, location)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	groupBy, groupKind := orgOverviewUsageGrouping(params.GroupBy)
	groupLimit := orgOverviewUsageDefaultGroupLimit
	if params.Limit != nil {
		groupLimit = *params.Limit
	}
	requestedProjectIDs, err := usageProjectIDsFromParams("project_ids", params.ProjectIds)
	if err != nil {
		return nil, err
	}
	projectIDs, err := s.readableProjectIDs(ctx, org, identitystore.ProjectActionRead, requestedProjectIDs)
	if err != nil {
		return nil, err
	}
	series, err := s.server.store.Execution().SumOrgModelUsageSeries(ctx, executionstore.SumOrgModelUsageSeriesInput{
		OrgID:          org.ID,
		ProjectIDs:     projectIDs,
		Window:         window,
		IntervalStarts: intervalStarts,
		GroupBy:        groupBy,
		GroupLimit:     groupLimit,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	previous, err := s.previousUsageTotals(ctx, org.ID, projectIDs, params.Since, end)
	if err != nil {
		return nil, err
	}
	response, err := orgOverviewUsageResponse(series, previous, groupKind)
	if err != nil {
		return nil, err
	}
	return openapi.GetOrgOverviewUsage200JSONResponse(response), nil
}

func orgOverviewUsageGrouping(
	groupBy *openapi.GetOrgOverviewUsageParamsGroupBy,
) (executionstore.UsageGroupBy, publicid.Kind) {
	if groupBy == nil {
		return executionstore.UsageGroupByModel, publicid.KindConfiguredModel
	}
	switch *groupBy {
	case openapi.GetOrgOverviewUsageParamsGroupByProject:
		return executionstore.UsageGroupByProject, publicid.KindProject
	case openapi.GetOrgOverviewUsageParamsGroupByProfile:
		return executionstore.UsageGroupByProfile, publicid.KindAgentProfile
	default:
		return executionstore.UsageGroupByModel, publicid.KindConfiguredModel
	}
}

func orgOverviewUsageLocation(timezone *string) (*time.Location, error) {
	if timezone == nil {
		return time.UTC, nil
	}
	if *timezone == "" || *timezone == "Local" {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid timezone")
	}
	location, err := time.LoadLocation(*timezone)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid timezone")
	}
	return location, nil
}

func (s strictOpenAPIServer) readableProjectIDs(
	ctx context.Context,
	org identitystore.OrgRecord,
	action string,
	requested []uuid.UUID,
) ([]uuid.UUID, error) {
	principal, _ := principalFromContext(ctx)
	wanted := make(map[uuid.UUID]bool, len(requested))
	for _, id := range requested {
		wanted[id] = true
	}
	projectIDs := []uuid.UUID{}
	after := listing.KeysetCursor{}
	for {
		page, err := s.server.store.Identity().ListVisibleProjectsForPrincipal(
			ctx,
			identitystore.ListVisibleProjectsForPrincipalInput{
				OrgID: org.ID, Principal: principal, Limit: orgOverviewPageSize, After: after,
			},
		)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		for _, record := range page.Projects {
			if !identitystore.ProjectRolesAllow(record.Roles, action) {
				continue
			}
			if len(wanted) > 0 && !wanted[record.Project.ID] {
				continue
			}
			projectIDs = append(projectIDs, record.Project.ID)
		}
		if !page.HasMore || len(page.Projects) == 0 {
			return projectIDs, nil
		}
		last := page.Projects[len(page.Projects)-1].Project
		after = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
}

func (s strictOpenAPIServer) previousUsageTotals(
	ctx context.Context,
	orgID uuid.UUID,
	projectIDs []uuid.UUID,
	since, end time.Time,
) (executionstore.ModelUsageTotals, error) {
	if len(projectIDs) == 0 {
		return executionstore.ModelUsageTotals{ProviderReportedCostUSD: "0"}, nil
	}
	previousSince := since.Add(-end.Sub(since))
	records, err := s.server.store.Execution().SumOrgModelUsage(ctx, executionstore.SumOrgModelUsageInput{
		OrgID:             orgID,
		Window:            executionstore.UsageWindow{Since: &previousSince, Until: &since},
		IncludeProjectIDs: projectIDs,
	})
	if err != nil {
		return executionstore.ModelUsageTotals{}, apierror.OrgScoped(err)
	}
	return executionstore.SumModelUsageTotals(records)
}

func orgOverviewUsageResponse(
	series executionstore.ModelUsageSeries,
	previous executionstore.ModelUsageTotals,
	groupKind publicid.Kind,
) (openapi.OrgOverviewUsageResponse, error) {
	groupIDs := make(map[uuid.UUID]*string, len(series.Groups))
	groups := make([]openapi.OrgOverviewUsageGroup, 0, len(series.Groups))
	for _, group := range series.Groups {
		response := openapi.OrgOverviewUsageGroup{Totals: usageTotalsResponse(group.Totals)}
		if group.ID != uuid.Nil {
			id, err := publicID(groupKind, group.ID)
			if err != nil {
				return openapi.OrgOverviewUsageResponse{}, err
			}
			response.Id, response.Name = new(id), new(group.Name)
		}
		groupIDs[group.ID] = response.Id
		groups = append(groups, response)
	}
	intervals := make([]openapi.OrgOverviewUsageInterval, 0, len(series.Intervals))
	for _, interval := range series.Intervals {
		intervalGroups := make([]openapi.OrgOverviewUsageIntervalGroup, 0, len(interval.Groups))
		for _, group := range interval.Groups {
			intervalGroups = append(intervalGroups, openapi.OrgOverviewUsageIntervalGroup{
				Id: groupIDs[group.GroupID], Totals: usageTotalsResponse(group.Totals),
			})
		}
		intervals = append(intervals, openapi.OrgOverviewUsageInterval{
			Start: interval.Start.UTC(), Totals: usageTotalsResponse(interval.Totals), Groups: intervalGroups,
		})
	}
	return openapi.OrgOverviewUsageResponse{
		Totals:         usageTotalsResponse(series.Totals),
		PreviousTotals: usageTotalsResponse(previous),
		ActiveAgents:   series.ActiveAgents,
		Groups:         groups,
		Intervals:      intervals,
	}, nil
}

func (s strictOpenAPIServer) GetOrgOverviewActivity(
	ctx context.Context,
	request openapi.GetOrgOverviewActivityRequestObject,
) (openapi.GetOrgOverviewActivityResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	window, err := usageWindowFromParams(&request.Params.Since, request.Params.Until)
	if err != nil {
		return nil, err
	}
	projectIDs, err := s.readableProjectIDs(ctx, org, identitystore.AgentActionRead, nil)
	if err != nil {
		return nil, err
	}
	activity, err := s.server.store.Execution().CountOrgActivity(ctx, executionstore.CountOrgActivityInput{
		OrgID:      org.ID,
		ProjectIDs: projectIDs,
		Window:     window,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapi.GetOrgOverviewActivity200JSONResponse(openapi.OrgOverviewActivityResponse{
		AgentsCreated: activity.AgentsCreated,
		MessagesSent:  activity.MessagesSent,
		TokensUsed:    activity.TokensUsed,
	}), nil
}
