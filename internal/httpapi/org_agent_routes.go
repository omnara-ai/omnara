package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const orgListProjectPageSize = 500

func (s strictOpenAPIServer) ListOrgAgents(
	ctx context.Context,
	request openapi.ListOrgAgentsRequestObject,
) (openapi.ListOrgAgentsResponseObject, error) {
	scope, err := s.orgListScope(ctx, identitystore.AgentActionRead)
	if err != nil {
		return nil, err
	}
	response, err := s.listAgents(ctx, openapi.ListAgentsParams(request.Params), scope)
	if err != nil {
		return nil, err
	}
	return openapi.ListOrgAgents200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListOrgAgentProfiles(
	ctx context.Context,
	request openapi.ListOrgAgentProfilesRequestObject,
) (openapi.ListOrgAgentProfilesResponseObject, error) {
	scope, err := s.orgListScope(ctx, identitystore.ProjectActionRead)
	if err != nil {
		return nil, err
	}
	response, err := s.listAgentProfiles(ctx, openapi.ListAgentProfilesParams(request.Params), scope)
	if err != nil {
		return nil, err
	}
	return openapi.ListOrgAgentProfiles200JSONResponse(response), nil
}

func (s strictOpenAPIServer) orgListScope(ctx context.Context, action string) (resourceListScope, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return resourceListScope{}, err
	}
	principal, _ := principalFromContext(ctx)
	projectIDs := []uuid.UUID{}
	after := listing.KeysetCursor{}
	for {
		page, err := s.server.store.Identity().ListVisibleProjectsForPrincipal(
			ctx,
			identitystore.ListVisibleProjectsForPrincipalInput{
				OrgID: org.ID, Principal: principal, Limit: orgListProjectPageSize, After: after,
			},
		)
		if err != nil {
			return resourceListScope{}, apierror.OrgScoped(err)
		}
		for _, record := range page.Projects {
			if identitystore.ProjectRolesAllow(record.Roles, action) {
				projectIDs = append(projectIDs, record.Project.ID)
			}
		}
		if !page.HasMore || len(page.Projects) == 0 {
			return resourceListScope{key: org.ID.String(), projectIDs: projectIDs}, nil
		}
		last := page.Projects[len(page.Projects)-1].Project
		after = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
}
