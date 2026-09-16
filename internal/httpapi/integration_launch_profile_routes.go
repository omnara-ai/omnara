package httpapi

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s strictOpenAPIServer) GetIntegrationLaunchProfile(
	ctx context.Context, request openapi.GetIntegrationLaunchProfileRequestObject,
) (openapi.GetIntegrationLaunchProfileResponseObject, error) {
	install, _, err := s.integrationLaunchProfileOwner(ctx, request.IntegrationInstallID)
	if err != nil {
		return nil, err
	}
	route, err := s.server.store.Integrations().GetIntegrationRouteByDeploymentKey(
		ctx, install.ProjectID, install.ID, install.Provider)
	if err != nil && !errors.Is(err, storeerr.ErrNotFound) {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationLaunchProfileResponse(route.AgentProfileID)
	return openapi.GetIntegrationLaunchProfile200JSONResponse(response), err
}

func (s strictOpenAPIServer) SetIntegrationLaunchProfile(
	ctx context.Context, request openapi.SetIntegrationLaunchProfileRequestObject,
) (openapi.SetIntegrationLaunchProfileResponseObject, error) {
	install, behavior, err := s.integrationLaunchProfileOwner(ctx, request.IntegrationInstallID)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	var profileID uuid.UUID
	if !request.Body.AgentProfileId.IsNull() {
		raw, err := request.Body.AgentProfileId.Get()
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "agent_profile_id is required; use null to clear")
		}
		profileID, err = publicid.Decode(publicid.KindAgentProfile, raw)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_profile_id")
		}
		profile, err := s.server.store.Execution().GetAgentProfile(ctx, install.ProjectID, profileID)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		if err := s.server.validateChannelSetupModel(ctx, profile.CurrentConfig); err != nil {
			return nil, err
		}
	}
	input := integrationstore.CreateIntegrationRouteInput{
		ProjectID: install.ProjectID, IntegrationInstallID: install.ID, AgentProfileID: profileID,
		DeploymentKey: install.Provider, BehaviorKey: behavior, State: integrationstore.IntegrationRouteStateActive,
	}
	route, err := s.server.store.Integrations().SetIntegrationRouteProfile(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationLaunchProfileResponse(route.AgentProfileID)
	return openapi.SetIntegrationLaunchProfile200JSONResponse(response), err
}

func (s strictOpenAPIServer) integrationLaunchProfileOwner(
	ctx context.Context, raw string,
) (integrationstore.IntegrationInstallRecord, string, error) {
	var install integrationstore.IntegrationInstallRecord
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return install, "", err
	}
	id, err := publicid.Decode(publicid.KindIntegrationInstall, raw)
	if err != nil {
		return install, "", apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	install, err = s.server.store.Integrations().GetIntegrationInstall(ctx, scope.project.ID, id)
	if err != nil {
		return install, "", apierror.ProjectScoped(err)
	}
	behavior := builtInIntegrationBehavior(install.Provider)
	if install.IntegrationKind != integrationstore.IntegrationKindManaged || behavior == "" {
		return install, "", apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	app, err := s.server.store.Integrations().GetIntegrationApp(ctx, scope.project.OrgID, install.IntegrationAppID)
	if err != nil {
		return install, "", apierror.ProjectScoped(err)
	}
	if app.ConnectorKey != channelconnector.BuiltInConnectorKey {
		return install, "", apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return install, behavior, nil
}

func builtInIntegrationBehavior(provider string) string {
	switch provider {
	case integrationstore.IntegrationProviderSlack:
		return "slack_conversation"
	case integrationstore.IntegrationProviderDiscord:
		return "discord_conversation"
	case integrationstore.IntegrationProviderGitHub:
		return "github_pr"
	default:
		return ""
	}
}

func integrationLaunchProfileResponse(profileID uuid.UUID) (openapi.IntegrationLaunchProfile, error) {
	id, err := idOrNil(publicid.KindAgentProfile, profileID)
	return openapi.IntegrationLaunchProfile{AgentProfileId: nullableFromPtr(id)}, err
}
