package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) CreateProjectIntegration(
	ctx context.Context,
	request openapi.CreateProjectIntegrationRequestObject,
) (openapi.CreateProjectIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	input, err := parseProjectIntegrationRequest(request.Body, scope.project.OrgID, scope.project.ID)
	if err != nil {
		return nil, err
	}
	integration, err := s.server.store.Integrations().CreateProjectIntegration(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectIntegrationResponse(integration)
	if err != nil {
		return nil, err
	}
	return openapi.CreateProjectIntegration201JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateProjectIntegration(
	ctx context.Context,
	request openapi.UpdateProjectIntegrationRequestObject,
) (openapi.UpdateProjectIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProjectIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	input, err := parseProjectIntegrationRequest(request.Body, scope.project.OrgID, scope.project.ID)
	if err != nil {
		return nil, err
	}
	integration, err := s.server.store.Integrations().UpdateProjectIntegration(ctx, id, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectIntegrationResponse(integration)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateProjectIntegration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetProjectIntegration(
	ctx context.Context,
	request openapi.GetProjectIntegrationRequestObject,
) (openapi.GetProjectIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProjectIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	integration, err := s.server.store.Integrations().GetProjectIntegration(ctx, scope.project.ID, id)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectIntegrationResponse(integration)
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
		response.RuntimeFailure = &openapi.ProjectIntegrationRuntimeFailure{
			Message: failure.Message, RetryAt: failure.RetryAt,
		}
	}
	return openapi.GetProjectIntegration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteProjectIntegration(
	ctx context.Context,
	request openapi.DeleteProjectIntegrationRequestObject,
) (openapi.DeleteProjectIntegrationResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProjectIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	if err := s.server.store.Integrations().DeleteProjectIntegration(
		ctx,
		scope.project.OrgID,
		scope.project.ID,
		id,
	); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteProjectIntegration204Response{}, nil
}

func (s strictOpenAPIServer) ListProjectIntegrations(
	ctx context.Context,
	request openapi.ListProjectIntegrationsRequestObject,
) (openapi.ListProjectIntegrationsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindProjectIntegration,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Integrations().ListProjectIntegrations(ctx, integrationstore.ListProjectIntegrationsInput{
		ProjectID: scope.project.ID, Limit: limit, After: after,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.ProjectIntegration, 0, len(page.Integrations))
	for _, integration := range page.Integrations {
		response, err := projectIntegrationResponse(integration)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeNextCursor(page.HasMore, page.Next.CreatedAt, publicid.KindProjectIntegration, page.Next.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListProjectIntegrations200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func parseProjectIntegrationRequest(
	body *openapi.SaveProjectIntegrationRequest,
	orgID,
	projectID uuid.UUID,
) (integrationstore.SaveProjectIntegrationInput, error) {
	if body == nil {
		return integrationstore.SaveProjectIntegrationInput{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"request body is required",
		)
	}
	input := integrationstore.SaveProjectIntegrationInput{
		OrgID:           orgID,
		ProjectID:       projectID,
		Name:            body.Name,
		IntegrationType: integrationdefinition.Type(body.IntegrationType),
	}
	if source := body.Settings.Launcher; source != nil {
		if input.IntegrationType == integrationdefinition.DiscordThread &&
			(source.ScopeKind != nil || source.ScopeRef != nil) {
			return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
				"Discord launchers do not accept scope_kind or scope_ref; manage bot access in Discord")
		}
		launcher := &integrationstore.IntegrationLauncher{
			Trigger:   source.Trigger,
			ScopeKind: stringFromPtr(source.ScopeKind),
			ScopeRef:  stringFromPtr(source.ScopeRef),
		}
		for _, sourceSlot := range source.Slots {
			slot := integrationstore.IntegrationLaunchSlot{Key: sourceSlot.Key}
			if sourceSlot.AgentProfileId != nil {
				id, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, *sourceSlot.AgentProfileId)
				if !ok {
					return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid launch slot agent_profile_id")
				}
				slot.AgentProfileID = &id
			}
			if sourceSlot.AgentId != nil {
				id, ok := parseOpenAPIPublicID(publicid.KindAgent, *sourceSlot.AgentId)
				if !ok {
					return input, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid launch slot agent_id")
				}
				slot.AgentID = &id
			}
			launcher.Slots = append(launcher.Slots, slot)
		}
		input.Settings.Launcher = launcher
	}
	return input, nil
}

func projectIntegrationResponse(
	integration integrationstore.ProjectIntegrationRecord,
) (openapi.ProjectIntegration, error) {
	id, err := publicID(publicid.KindProjectIntegration, integration.ID)
	if err != nil {
		return openapi.ProjectIntegration{}, err
	}
	projectID, err := publicID(publicid.KindProject, integration.ProjectID)
	if err != nil {
		return openapi.ProjectIntegration{}, err
	}
	response := openapi.ProjectIntegration{
		Id:                       id,
		ProjectId:                projectID,
		Name:                     integration.Name,
		IntegrationType:          openapi.IntegrationType(integration.IntegrationType),
		State:                    openapi.ProjectIntegrationState(integration.State),
		SetupRevision:            integration.SetupRevision,
		ProviderTenantId:         integration.ProviderTenantID,
		ProviderAccountRef:       integration.ProviderAccountRef,
		ProviderAgentDisplayName: integration.ProviderAgentDisplayName,
		CreatedAt:                integration.CreatedAt,
		UpdatedAt:                integration.UpdatedAt,
	}
	response.CredentialSecretId, err = idOrNil(publicid.KindSecret, integration.CredentialSecretID)
	if err != nil {
		return openapi.ProjectIntegration{}, err
	}
	if err := json.Unmarshal(integration.ProviderConfig, &response.ProviderConfig); err != nil {
		return openapi.ProjectIntegration{}, err
	}
	response.LastOauthFlowId, err = idOrNil(publicid.KindIntegrationOAuthFlow, integration.LastOAuthFlowID)
	if err != nil {
		return openapi.ProjectIntegration{}, err
	}
	response.Capabilities, err = integrationCapabilitiesResponse(integration.IntegrationType)
	if err != nil {
		return openapi.ProjectIntegration{}, err
	}
	if source := integration.Settings.Launcher; source != nil {
		launcher := &openapi.IntegrationLauncher{
			Trigger:   source.Trigger,
			ScopeKind: ptrFromNonEmpty(source.ScopeKind),
			ScopeRef:  ptrFromNonEmpty(source.ScopeRef),
			Slots:     make([]openapi.IntegrationLaunchSlot, 0, len(source.Slots)),
		}
		for _, sourceSlot := range source.Slots {
			slot := openapi.IntegrationLaunchSlot{Key: sourceSlot.Key}
			if sourceSlot.AgentProfileID != nil {
				id, err := publicID(publicid.KindAgentProfile, *sourceSlot.AgentProfileID)
				if err != nil {
					return openapi.ProjectIntegration{}, err
				}
				slot.AgentProfileId = &id
			}
			if sourceSlot.AgentID != nil {
				id, err := publicID(publicid.KindAgent, *sourceSlot.AgentID)
				if err != nil {
					return openapi.ProjectIntegration{}, err
				}
				slot.AgentId = &id
			}
			launcher.Slots = append(launcher.Slots, slot)
		}
		response.Settings.Launcher = launcher
	}
	return response, nil
}
