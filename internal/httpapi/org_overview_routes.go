package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const (
	// orgOverviewRecentLimit is the number of recent agents and profiles returned.
	orgOverviewRecentLimit = 5
	// orgOverviewMaxProjects caps how many visible projects the overview
	// considers (and returns); recents beyond this cap are best-effort omitted.
	orgOverviewMaxProjects = 200
	orgOverviewProjectPage = 100
)

func (s strictOpenAPIServer) GetOrgOverview(
	ctx context.Context,
	_ openapi.GetOrgOverviewRequestObject,
) (openapi.GetOrgOverviewResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	visible, err := s.visibleProjectsForOverview(ctx, org, principal)
	if err != nil {
		return nil, err
	}
	projects := make([]openapi.VisibleProject, 0, len(visible))
	agentProjectIDs := make([]storage.ID, 0, len(visible))
	profileProjectIDs := make([]storage.ID, 0, len(visible))
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
	recentProfiles := make([]openapi.AgentProfile, 0, len(profileRecords))
	for _, record := range profileRecords {
		response, err := s.server.agentProfileResponseFromRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		recentProfiles = append(recentProfiles, response)
	}
	return openapi.GetOrgOverview200JSONResponse(openapi.OrgOverviewResponse{
		Projects:            projects,
		RecentAgents:        recentAgents,
		RecentAgentProfiles: recentProfiles,
	}), nil
}

func (s strictOpenAPIServer) visibleProjectsForOverview(
	ctx context.Context,
	org identitystore.OrgRecord,
	principal identitystore.PrincipalRecord,
) ([]identitystore.VisibleProjectRecord, error) {
	records := make([]identitystore.VisibleProjectRecord, 0)
	after := listing.KeysetCursor{}
	for len(records) < orgOverviewMaxProjects {
		limit := orgOverviewProjectPage
		if remaining := orgOverviewMaxProjects - len(records); remaining < limit {
			limit = remaining
		}
		page, err := s.server.store.Identity().ListVisibleProjectsForPrincipal(
			ctx,
			identitystore.ListVisibleProjectsForPrincipalInput{
				OrgID: org.ID, Principal: principal, Limit: limit, After: after,
			},
		)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		records = append(records, page.Projects...)
		if !page.HasMore || len(page.Projects) == 0 {
			break
		}
		last := page.Projects[len(page.Projects)-1]
		after = listing.KeysetCursor{Set: true, CreatedAt: last.Project.CreatedAt, ID: last.Project.ID}
	}
	return records, nil
}
