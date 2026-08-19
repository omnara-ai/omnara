package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) ListIntegrationInstalls(
	ctx context.Context,
	request openapi.ListIntegrationInstallsRequestObject,
) (openapi.ListIntegrationInstallsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := integrationstore.IntegrationInstallListFilters{}
	if request.Params.AgentProfileId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, *request.Params.AgentProfileId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_profile_id")
		}
		filters.AgentProfileID = id
	}
	if request.Params.OauthFlowId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindIntegrationOAuthFlow, *request.Params.OauthFlowId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid oauth_flow_id")
		}
		filters.OAuthFlowID = id
	}
	extra := struct{ AgentProfileID, OAuthFlowID string }{}
	if filters.AgentProfileID != storage.NilID {
		extra.AgentProfileID = filters.AgentProfileID.String()
	}
	if filters.OAuthFlowID != storage.NilID {
		extra.OAuthFlowID = filters.OAuthFlowID.String()
	}
	scopeKey := scope.project.OrgID.String() + "/" + scope.project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "integration_installs",
		Scope: scopeKey, IDKind: publicid.KindIntegrationInstall,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Integrations().ListIntegrationInstallsForProject(
		ctx,
		integrationstore.ListIntegrationInstallsForProjectInput{
			ProjectID: scope.project.ID,
			Filters:   filters,
			List:      list,
			Limit:     limit,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.IntegrationInstall, 0, len(page.Installs))
	for _, install := range page.Installs {
		response, err := integrationInstallResponse(install)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "integration_installs", scopeKey,
		publicid.KindIntegrationInstall, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListIntegrationInstalls200JSONResponse(openapi.ListIntegrationInstallsResponse{
		Data:       data,
		NextCursor: nullableFromPtr(next),
	}), nil
}

func integrationInstallResponse(record integrationstore.IntegrationInstallRecord) (openapi.IntegrationInstall, error) {
	id, err := publicID(publicid.KindIntegrationInstall, record.ID)
	if err != nil {
		return openapi.IntegrationInstall{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.IntegrationInstall{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.IntegrationInstall{}, err
	}
	agentProfileID, err := idOrNil(publicid.KindAgentProfile, record.AgentProfileID)
	if err != nil {
		return openapi.IntegrationInstall{}, err
	}
	agentID, err := idOrNil(publicid.KindAgent, record.AgentID)
	if err != nil {
		return openapi.IntegrationInstall{}, err
	}
	return openapi.IntegrationInstall{
		Id:                       id,
		OrgId:                    orgID,
		ProjectId:                projectID,
		AgentProfileId:           agentProfileID,
		AgentId:                  agentID,
		Provider:                 record.Provider,
		IntegrationKind:          record.IntegrationKind,
		ConnectionMode:           record.ConnectionMode,
		State:                    openapi.IntegrationInstallState(record.State),
		ProviderTenantId:         record.ProviderTenantID,
		ProviderAccountRef:       record.ProviderAccountRef,
		ProviderAgentDisplayName: record.ProviderAgentDisplayName,
		CreatedAt:                record.CreatedAt,
		UpdatedAt:                record.UpdatedAt,
	}, nil
}

func (s strictOpenAPIServer) DeleteIntegrationInstall(
	ctx context.Context,
	request openapi.DeleteIntegrationInstallRequestObject,
) (openapi.DeleteIntegrationInstallResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	installID, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Integrations().DeleteIntegrationInstall(ctx, scope.project.ID, installID); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteIntegrationInstall204Response{}, nil
}
