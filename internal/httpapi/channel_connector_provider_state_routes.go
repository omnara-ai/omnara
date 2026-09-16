package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) ListChannelConnectorInstallationControlScopes(
	ctx context.Context, request openapi.ListChannelConnectorInstallationControlScopesRequestObject,
) (openapi.ListChannelConnectorInstallationControlScopesResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	after, err := optionalControlInstallationID(request.Params.AfterInstallationId)
	if err != nil {
		return nil, err
	}
	through, err := optionalControlInstallationID(request.Params.ThroughInstallationId)
	if err != nil {
		return nil, err
	}
	var afterID uuid.UUID
	if after != nil {
		afterID = *after
	}
	page, err := s.server.store.Integrations().ListConnectorInstallationControlScopes(ctx,
		integrationstore.ListConnectorInstallationControlScopesInput{
			IntegrationAppID: appID, ProviderTenantID: request.Params.ProviderTenantId, Limit: int32(limit),
			AfterID: afterID, ThroughID: through, Capabilities: scope.Capabilities,
		})
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response := openapi.ListChannelConnectorInstallationControlScopesResponse{
		AppConfigurationRevision: page.AppConfigurationRevision,
		Installations:            make([]openapi.ChannelConnectorInstallationControlScope, 0, len(page.Installations)),
	}
	var next *string
	for _, install := range page.Installations {
		id, err := publicID(publicid.KindIntegrationInstall, install.ID)
		if err != nil {
			return nil, err
		}
		projectID, err := publicID(publicid.KindProject, install.ProjectID)
		if err != nil {
			return nil, err
		}
		response.Installations = append(response.Installations, openapi.ChannelConnectorInstallationControlScope{
			Id: id, ProjectId: projectID, ProviderTenantId: install.ProviderTenantID,
			ProviderAccountRef: install.ProviderAccountRef, ConfigurationRevision: install.ConfigurationRevision,
			State:            openapi.ChannelConnectorInstallationProviderState(install.State),
			ProviderIdentity: install.ProviderIdentity,
		})
		if page.HasMore {
			next = &id
		}
	}
	end, err := idOrNil(publicid.KindIntegrationInstall, page.ThroughID)
	if err != nil {
		return nil, err
	}
	response.ThroughInstallationId = nullableFromPtr(end)
	response.NextAfterInstallationId = nullableFromPtr(next)
	return openapi.ListChannelConnectorInstallationControlScopes200JSONResponse(response), nil
}

func (s strictOpenAPIServer) SetChannelConnectorInstallationProviderState(
	ctx context.Context, request openapi.SetChannelConnectorInstallationProviderStateRequestObject,
) (openapi.SetChannelConnectorInstallationProviderStateResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	appID, appOK := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	installID, installOK := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	if !appOK || !installOK {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	body := *request.Body
	// This control path intentionally does not use the active-only installation
	// configuration/receipt helper. The owning store rechecks all other authority.
	result, err := s.server.store.Integrations().SetConnectorInstallationProviderState(ctx,
		integrationstore.SetConnectorInstallationProviderStateInput{
			IntegrationAppID: appID, IntegrationInstallID: installID,
			ProviderTenantID: body.ProviderTenantId, ProviderAccountRef: body.ProviderAccountRef,
			ExpectedAppConfigurationRevision: body.ExpectedAppConfigurationRevision,
			ExpectedConfigurationRevision:    body.ExpectedConfigurationRevision,
			State:                            integrationstore.IntegrationInstallState(body.State),
			Capabilities:                     scope.Capabilities,
		})
	if err != nil {
		return nil, apierror.FromError(err)
	}
	return openapi.SetChannelConnectorInstallationProviderState200JSONResponse{
		State:                 openapi.ChannelConnectorInstallationProviderState(result.State),
		ConfigurationRevision: result.ConfigurationRevision,
	}, nil
}
