package httpapi

import (
	"context"
	"slices"
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
	orgOverviewRecentLimit = 5
	// orgOverviewMaxProjects caps how many visible projects the overview
	// considers (and returns); recents beyond this cap are best-effort omitted.
	orgOverviewMaxProjects = 200
)

func (s strictOpenAPIServer) GetOrgOverview(
	ctx context.Context,
	request openapi.GetOrgOverviewRequestObject,
) (openapi.GetOrgOverviewResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	location, err := timezoneLocation(request.Params.Timezone)
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
	readableProjectIDs := make([]uuid.UUID, 0, len(visible))
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
			readableProjectIDs = append(readableProjectIDs, record.Project.ID)
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
			ProjectIDs: readableProjectIDs, Limit: orgOverviewRecentLimit,
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
		ctx, slices.Concat(readableProjectIDs, agentProjectIDs), agentRecords, profileRecords,
	)
	if err != nil {
		return nil, err
	}
	todayStart := executionstore.UsageDayStart(time.Now(), location)
	activity, err := s.server.store.Execution().CountOrgActivity(ctx, executionstore.CountOrgActivityInput{
		ProjectIDs: agentProjectIDs, Window: executionstore.UsageWindow{Since: &todayStart},
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapi.GetOrgOverview200JSONResponse(openapi.OrgOverviewResponse{
		Projects:                projects,
		RecentAgents:            recentAgents,
		RecentAgentProfiles:     recentProfiles,
		ReferencedAgentProfiles: referencedProfiles,
		Today: openapi.OrgOverviewToday{
			AgentsCreated: activity.AgentsCreated,
			MessagesSent:  activity.MessagesSent,
		},
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
