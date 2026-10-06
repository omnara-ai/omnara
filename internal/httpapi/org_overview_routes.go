package httpapi

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const (
	orgOverviewRecentLimit = 5
	// orgOverviewMaxProjects caps how many projects the overview returns. Recents
	// and activity still cover every visible project, and the most recently
	// active projects are the ones returned.
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
	visible, err := s.listAllVisibleProjects(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	agentProjectIDs := make([]uuid.UUID, 0, len(visible))
	readableProjectIDs := make([]uuid.UUID, 0, len(visible))
	for _, record := range visible {
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
	activity, err := s.server.store.Execution().ListProjectLastActivity(
		ctx,
		executionstore.ListProjectLastActivityInput{
			AgentProjectIDs: agentProjectIDs, ProfileProjectIDs: readableProjectIDs,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	listed := mostRecentlyActiveProjects(visible, activity, orgOverviewMaxProjects)
	projects := make([]openapi.VisibleProject, 0, len(listed))
	projectActivity := make([]openapi.OrgOverviewProjectActivity, 0, len(listed))
	for _, record := range listed {
		response, err := visibleProjectResponse(record)
		if err != nil {
			return nil, err
		}
		projects = append(projects, response)
		lastActiveAt, ok := activity[record.Project.ID]
		if !ok {
			continue
		}
		projectActivity = append(projectActivity, openapi.OrgOverviewProjectActivity{
			ProjectId: response.Id, LastActiveAt: lastActiveAt.UTC(),
		})
	}
	return openapi.GetOrgOverview200JSONResponse(openapi.OrgOverviewResponse{
		Projects:                projects,
		ProjectActivity:         projectActivity,
		RecentAgents:            recentAgents,
		RecentAgentProfiles:     recentProfiles,
		ReferencedAgentProfiles: referencedProfiles,
	}), nil
}

// mostRecentlyActiveProjects returns up to limit projects, most recently
// active first, counting a project's own updates as activity too.
func mostRecentlyActiveProjects(
	visible []identitystore.VisibleProjectRecord,
	activity map[uuid.UUID]time.Time,
	limit int,
) []identitystore.VisibleProjectRecord {
	activeAt := func(record identitystore.VisibleProjectRecord) time.Time {
		if at, ok := activity[record.Project.ID]; ok && at.After(record.Project.UpdatedAt) {
			return at
		}
		return record.Project.UpdatedAt
	}
	sorted := slices.Clone(visible)
	slices.SortStableFunc(sorted, func(left, right identitystore.VisibleProjectRecord) int {
		return cmp.Compare(activeAt(right).UnixNano(), activeAt(left).UnixNano())
	})
	return sorted[:min(limit, len(sorted))]
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
