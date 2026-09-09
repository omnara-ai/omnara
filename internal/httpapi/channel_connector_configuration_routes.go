package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type channelConnectorScope struct {
	ID           string
	Capabilities []channelconnector.Capability
}

const channelConfigurationReadAttempts = 3

func channelConnectorScopeFromContext(ctx context.Context) (channelConnectorScope, error) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeChannelConnector ||
		principal.ChannelConnectorID == "" ||
		len(principal.ChannelConnectorCapabilities) == 0 {
		return channelConnectorScope{}, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	return channelConnectorScope{
		ID:           principal.ChannelConnectorID,
		Capabilities: principal.ChannelConnectorCapabilities,
	}, nil
}

func (scope channelConnectorScope) authorizeClaimCapability(
	requested openapi.ChannelConnectorCapability,
) (channelconnector.Capability, error) {
	capabilities, err := channelconnector.NormalizeCapabilities([]channelconnector.Capability{{
		ConnectorKey: requested.ConnectorKey,
		Provider:     requested.Provider,
	}})
	if err != nil {
		return channelconnector.Capability{}, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			err.Error(),
		)
	}
	for _, capability := range scope.Capabilities {
		if capability == capabilities[0] {
			return capabilities[0], nil
		}
	}
	return channelconnector.Capability{}, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
}

func (s strictOpenAPIServer) GetChannelConnectorAppConfiguration(
	ctx context.Context,
	request openapi.GetChannelConnectorAppConfigurationRequestObject,
) (openapi.GetChannelConnectorAppConfigurationResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	response, err := s.channelConnectorAppConfiguration(ctx, scope, appID)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	return openapi.GetChannelConnectorAppConfiguration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetChannelConnectorInstallationConfiguration(
	ctx context.Context,
	request openapi.GetChannelConnectorInstallationConfigurationRequestObject,
) (openapi.GetChannelConnectorInstallationConfigurationResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	installID, ok := parseOpenAPIPublicID(
		publicid.KindIntegrationInstall,
		request.IntegrationInstallID,
	)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	response, err := s.channelConnectorInstallationConfiguration(
		ctx,
		scope,
		appID,
		func() (integrationstore.IntegrationInstallRecord, error) {
			return s.server.store.Integrations().GetConnectorIntegrationInstallByID(
				ctx, appID, installID,
			)
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	return openapi.GetChannelConnectorInstallationConfiguration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ResolveChannelConnectorInstallationConfiguration(
	ctx context.Context,
	request openapi.ResolveChannelConnectorInstallationConfigurationRequestObject,
) (openapi.ResolveChannelConnectorInstallationConfigurationResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindIntegrationApp, request.IntegrationAppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	response, err := s.channelConnectorInstallationConfiguration(
		ctx,
		scope,
		appID,
		func() (integrationstore.IntegrationInstallRecord, error) {
			return s.server.store.Integrations().GetConnectorIntegrationInstall(
				ctx,
				appID,
				request.Params.ExternalTenantId,
				request.Params.ExternalAccountRef,
			)
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	return openapi.ResolveChannelConnectorInstallationConfiguration200JSONResponse(response), nil
}

func (s strictOpenAPIServer) channelConnectorAppConfiguration(
	ctx context.Context,
	scope channelConnectorScope,
	appID integrationstore.ID,
) (openapi.ChannelConnectorAppConfiguration, error) {
	for range channelConfigurationReadAttempts {
		app, err := s.server.store.Integrations().GetConnectorIntegrationApp(
			ctx, appID, scope.Capabilities,
		)
		if err != nil {
			return openapi.ChannelConnectorAppConfiguration{}, err
		}
		credential, credentialErr := s.channelConnectorAppCredential(ctx, app)
		current, currentErr := s.server.store.Integrations().GetConnectorIntegrationApp(
			ctx, appID, scope.Capabilities,
		)
		if currentErr != nil {
			return openapi.ChannelConnectorAppConfiguration{}, currentErr
		}
		if current.ConfigurationRevision != app.ConfigurationRevision {
			continue
		}
		if credentialErr != nil {
			return openapi.ChannelConnectorAppConfiguration{}, credentialErr
		}
		return channelConnectorAppConfigurationResponse(current, credential)
	}
	return openapi.ChannelConnectorAppConfiguration{}, storeerr.ErrStateTransitionConflict
}

func (s strictOpenAPIServer) channelConnectorInstallationConfiguration(
	ctx context.Context,
	scope channelConnectorScope,
	appID integrationstore.ID,
	resolve func() (integrationstore.IntegrationInstallRecord, error),
) (openapi.ChannelConnectorInstallationConfiguration, error) {
	for range channelConfigurationReadAttempts {
		app, err := s.server.store.Integrations().GetConnectorIntegrationApp(
			ctx, appID, scope.Capabilities,
		)
		if err != nil {
			return openapi.ChannelConnectorInstallationConfiguration{}, err
		}
		install, err := resolve()
		if err != nil {
			return openapi.ChannelConnectorInstallationConfiguration{}, err
		}
		credential, credentialErr := s.channelConnectorInstallationCredential(ctx, app, install)

		currentApp, appErr := s.server.store.Integrations().GetConnectorIntegrationApp(
			ctx, appID, scope.Capabilities,
		)
		if appErr != nil {
			return openapi.ChannelConnectorInstallationConfiguration{}, appErr
		}
		currentInstall, installErr := resolve()
		if installErr != nil {
			if currentApp.ConfigurationRevision != app.ConfigurationRevision ||
				storeerr.IsNotFound(installErr) {
				continue
			}
			return openapi.ChannelConnectorInstallationConfiguration{}, installErr
		}
		if currentApp.ConfigurationRevision != app.ConfigurationRevision ||
			currentInstall.ID != install.ID ||
			currentInstall.ConfigurationRevision != install.ConfigurationRevision {
			continue
		}
		if credentialErr != nil {
			return openapi.ChannelConnectorInstallationConfiguration{}, credentialErr
		}
		return channelConnectorInstallationConfigurationResponse(
			currentApp, currentInstall, credential,
		)
	}
	return openapi.ChannelConnectorInstallationConfiguration{}, storeerr.ErrStateTransitionConflict
}

func (s strictOpenAPIServer) channelConnectorAppCredential(
	ctx context.Context,
	app integrationstore.IntegrationAppRecord,
) (*openapi.ChannelCredentialPayload, error) {
	if app.CredentialSecretID == storage.NilID {
		return nil, nil //nolint:nilnil // No app credential is a valid optional configuration.
	}
	credential, err := s.server.store.Secrets().GetIntegrationAssociatedSecretPayload(
		ctx, app.OrgID, app.ID, app.CredentialSecretID,
	)
	if err != nil {
		return nil, err
	}
	encoded, err := channelCredentialPayloadJSON(credential.Payload)
	if err != nil {
		return nil, err
	}
	return &openapi.ChannelCredentialPayload{
		Kind: openapi.SecretKindResponse(credential.Kind), Payload: encoded,
	}, nil
}

func (s strictOpenAPIServer) channelConnectorInstallationCredential(
	ctx context.Context,
	app integrationstore.IntegrationAppRecord,
	install integrationstore.IntegrationInstallRecord,
) (*openapi.ChannelCredentialPayload, error) {
	if install.CredentialSecretID == storage.NilID {
		return nil, nil //nolint:nilnil // No install credential is a valid optional configuration.
	}
	payload, err := s.server.store.Secrets().GetProjectOwnedSecretPayload(
		ctx, install.OrgID, install.ProjectID, install.CredentialSecretID,
	)
	if err != nil {
		return nil, err
	}
	encoded, err := channelCredentialPayloadJSON(payload)
	if err != nil {
		return nil, err
	}
	return &openapi.ChannelCredentialPayload{
		Kind: app.InstallationCredentialKind, Payload: encoded,
	}, nil
}

func channelConnectorAppConfigurationResponse(
	app integrationstore.IntegrationAppRecord,
	credential *openapi.ChannelCredentialPayload,
) (openapi.ChannelConnectorAppConfiguration, error) {
	appID, err := publicID(publicid.KindIntegrationApp, app.ID)
	if err != nil {
		return openapi.ChannelConnectorAppConfiguration{}, err
	}
	response := openapi.ChannelConnectorAppConfiguration{
		App: openapi.ChannelConnectorApp{
			Id: appID, Provider: app.Provider, ProviderAppRef: app.ProviderAppRef,
			DisplayName: app.DisplayName, ConnectorKey: app.ConnectorKey,
			ProviderConfig: app.ProviderConfig, ProviderMetadata: app.ProviderMetadata,
			ConfigurationRevision: app.ConfigurationRevision, UpdatedAt: app.UpdatedAt,
		},
		Credential: credential,
	}
	if app.InstallationCredentialKind != "" {
		kind := app.InstallationCredentialKind
		response.App.InstallationCredentialKind = &kind
	}
	return response, nil
}

func channelConnectorInstallationConfigurationResponse(
	app integrationstore.IntegrationAppRecord,
	install integrationstore.IntegrationInstallRecord,
	credential *openapi.ChannelCredentialPayload,
) (openapi.ChannelConnectorInstallationConfiguration, error) {
	publicAppID, err := publicID(publicid.KindIntegrationApp, app.ID)
	if err != nil {
		return openapi.ChannelConnectorInstallationConfiguration{}, err
	}
	installID, err := publicID(publicid.KindIntegrationInstall, install.ID)
	if err != nil {
		return openapi.ChannelConnectorInstallationConfiguration{}, err
	}
	return openapi.ChannelConnectorInstallationConfiguration{
		IntegrationAppId: publicAppID, AppConfigurationRevision: app.ConfigurationRevision,
		Install: openapi.ChannelConnectorInstall{
			Id: installID, ProviderTenantId: install.ProviderTenantID,
			ProviderAccountRef:       install.ProviderAccountRef,
			ProviderAgentDisplayName: install.ProviderAgentDisplayName,
			ProviderConfig:           install.ProviderConfig,
			ProviderIdentity:         install.ProviderIdentity,
			ProviderMetadata:         install.ProviderMetadata,
			ConfigurationRevision:    install.ConfigurationRevision,
			UpdatedAt:                install.UpdatedAt,
		},
		Credential: credential,
	}, nil
}

func channelCredentialPayloadJSON(payload secrets.Payload) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode channel credential payload: %w", err)
	}
	return body, nil
}
