package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) CreateIntegration(
	ctx context.Context,
	request openapi.CreateIntegrationRequestObject,
) (openapi.CreateIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	input, err := parseIntegrationRequest(request.Body, scope.project.OrgID, scope.project.ID)
	if err != nil {
		return nil, err
	}
	integration, err := s.server.store.Integrations().CreateIntegration(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationResponse(integration)
	if err != nil {
		return nil, err
	}
	return openapi.CreateIntegration201JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateIntegration(
	ctx context.Context,
	request openapi.UpdateIntegrationRequestObject,
) (openapi.UpdateIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	settings, err := json.Marshal(request.Body.Settings)
	if err != nil {
		return nil, err
	}
	input := integrationstore.SaveIntegrationInput{
		OrgID: scope.project.OrgID, ProjectID: scope.project.ID, Settings: settings,
	}
	integration, err := s.server.store.Integrations().UpdateIntegration(ctx, id, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationResponse(integration)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateIntegration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetIntegration(
	ctx context.Context,
	request openapi.GetIntegrationRequestObject,
) (openapi.GetIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	integration, err := s.server.store.Integrations().GetIntegration(ctx, scope.project.ID, id)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationResponse(integration)
	if err != nil {
		return nil, err
	}
	failure, err := s.server.store.Integrations().GetIntegrationRuntimeFailure(
		ctx,
		integration.ProjectID,
		integration.ID,
		integration.SetupRevision,
	)
	if err != nil && !errors.Is(err, storeerr.ErrNotFound) {
		return nil, apierror.ProjectScoped(err)
	}
	if err == nil {
		response.RuntimeFailure = &openapi.IntegrationRuntimeFailure{
			Message: failure.Message, RetryAt: failure.RetryAt,
		}
	}
	return openapi.GetIntegration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteIntegration(
	ctx context.Context,
	request openapi.DeleteIntegrationRequestObject,
) (openapi.DeleteIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	if err := s.server.store.Integrations().DeleteIntegration(
		ctx,
		scope.project.OrgID,
		scope.project.ID,
		id,
	); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteIntegration204Response{}, nil
}

func (s strictOpenAPIServer) ListIntegrations(
	ctx context.Context,
	request openapi.ListIntegrationsRequestObject,
) (openapi.ListIntegrationsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindIntegration,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Integrations().ListIntegrations(ctx, integrationstore.ListIntegrationsInput{
		ProjectID: scope.project.ID, Limit: limit, After: after,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.Integration, 0, len(page.Integrations))
	for _, integration := range page.Integrations {
		response, err := integrationResponse(integration)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeNextCursor(page.HasMore, page.Next.CreatedAt, publicid.KindIntegration, page.Next.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListIntegrations200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func parseIntegrationRequest(
	body *openapi.CreateIntegrationRequest,
	orgID,
	projectID uuid.UUID,
) (integrationstore.SaveIntegrationInput, error) {
	if body == nil {
		return integrationstore.SaveIntegrationInput{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"request body is required",
		)
	}
	input := integrationstore.SaveIntegrationInput{
		OrgID:           orgID,
		ProjectID:       projectID,
		Name:            body.Name,
		IntegrationKind: integrationdefinition.Kind(body.IntegrationKind),
	}
	settings, err := json.Marshal(body.Settings)
	if err != nil {
		return input, err
	}
	input.Settings = settings
	return input, nil
}

func integrationResponse(
	integration integrationstore.IntegrationRecord,
) (openapi.Integration, error) {
	id, err := publicID(publicid.KindIntegration, integration.ID)
	if err != nil {
		return openapi.Integration{}, err
	}
	projectID, err := publicID(publicid.KindProject, integration.ProjectID)
	if err != nil {
		return openapi.Integration{}, err
	}
	response := openapi.Integration{
		Id:                       id,
		ProjectId:                projectID,
		Name:                     integration.Name,
		IntegrationKind:          openapi.IntegrationKind(integration.IntegrationKind),
		State:                    openapi.IntegrationState(integration.State),
		SetupRevision:            integration.SetupRevision,
		ProviderAgentDisplayName: integration.ProviderAgentDisplayName,
		CreatedAt:                integration.CreatedAt,
		UpdatedAt:                integration.UpdatedAt,
	}
	if integration.ProviderTenantID != "" {
		response.ProviderTenantId = &integration.ProviderTenantID
	}
	if integration.ProviderAccountRef != "" {
		response.ProviderAccountRef = &integration.ProviderAccountRef
	}
	if integration.Provider == integrationdefinition.ProviderGitHub {
		var identity github.AppIdentity
		if json.Unmarshal(integration.ProviderIdentity, &identity) == nil && identity.AppSlug != "" {
			mention := "@" + identity.AppSlug
			response.BotMention = &mention
		}
	}
	response.CredentialSecretId, err = idOrNil(publicid.KindSecret, integration.CredentialSecretID)
	if err != nil {
		return openapi.Integration{}, err
	}
	if err := json.Unmarshal(integration.ProviderConfig, &response.ProviderConfig); err != nil {
		return openapi.Integration{}, err
	}
	response.LastOauthFlowId, err = idOrNil(publicid.KindIntegrationOAuthFlow, integration.LastOAuthFlowID)
	if err != nil {
		return openapi.Integration{}, err
	}
	response.Capabilities, err = integrationCapabilitiesResponse(integration.IntegrationKind)
	if err != nil {
		return openapi.Integration{}, err
	}
	if err := json.Unmarshal(integration.Settings, &response.Settings); err != nil {
		return openapi.Integration{}, err
	}
	return response, nil
}
