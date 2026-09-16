package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

func (s strictOpenAPIServer) CreateIntegrationApp(
	ctx context.Context, request openapi.CreateIntegrationAppRequestObject,
) (openapi.CreateIntegrationAppResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	body := *request.Body
	body.ProviderAppRef = strings.TrimSpace(body.ProviderAppRef)
	if body.ProviderAppRef == "" || len(body.ProviderAppRef) > 512 || dbsafe.Text(body.ProviderAppRef) != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid provider_app_ref")
	}
	configuration, err := integrationAppConfiguration(string(body.Provider), body.ProviderConfig)
	if err != nil {
		return nil, err
	}
	credentialID, err := s.integrationAppCredential(ctx, org.ID, body.CredentialSecretId)
	if err != nil {
		return nil, err
	}
	var projectID uuid.UUID
	if body.OwnerProjectId != nil {
		projectID, err = publicid.Decode(publicid.KindProject, *body.OwnerProjectId)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner_project_id")
		}
	}
	name, err := resourcename.CanonicalizeRequired("integration app name", body.Name)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	credentialKind := ""
	if body.Provider == openapi.IntegrationAppProviderSlack {
		credentialKind = string(secretstore.SecretKindSlackAppCredentials)
	}
	app, err := s.server.store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: org.ID, OwnerProjectID: projectID, Provider: string(body.Provider),
		ProviderAppRef: body.ProviderAppRef, DisplayName: name, ConnectorKey: channelconnector.BuiltInConnectorKey,
		CredentialSecretID: credentialID, ProviderConfig: configuration, InstallationCredentialKind: credentialKind,
		State: integrationstore.IntegrationAppStateActive,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := publicIntegrationApp(app)
	return openapi.CreateIntegrationApp201JSONResponse(response), err
}

func (s strictOpenAPIServer) GetIntegrationApp(
	ctx context.Context, request openapi.GetIntegrationAppRequestObject,
) (openapi.GetIntegrationAppResponseObject, error) {
	app, err := s.integrationAppByPublicID(ctx, request.IntegrationAppID)
	if err != nil {
		return nil, err
	}
	response, err := publicIntegrationApp(app)
	return openapi.GetIntegrationApp200JSONResponse(response), err
}

func (s strictOpenAPIServer) UpdateIntegrationApp(
	ctx context.Context, request openapi.UpdateIntegrationAppRequestObject,
) (openapi.UpdateIntegrationAppResponseObject, error) {
	app, err := s.integrationAppByPublicID(ctx, request.IntegrationAppID)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	body := *request.Body
	input := integrationstore.UpdateIntegrationAppInput{OrgID: app.OrgID, ID: app.ID, DisplayName: body.Name}
	if body.Name != nil {
		name, err := resourcename.CanonicalizeRequired("integration app name", *body.Name)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		}
		input.DisplayName = &name
	}
	if body.CredentialSecretId != nil {
		id, err := s.integrationAppCredential(ctx, app.OrgID, *body.CredentialSecretId)
		if err != nil {
			return nil, err
		}
		input.CredentialSecretID = &id
	}
	if body.ProviderConfig != nil {
		input.ProviderConfig, err = integrationAppConfiguration(app.Provider, *body.ProviderConfig)
		if err != nil {
			return nil, err
		}
	}
	if body.State != nil {
		state := integrationstore.IntegrationAppState(*body.State)
		input.State = &state
	}
	app, err = s.server.store.Integrations().UpdateIntegrationApp(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := publicIntegrationApp(app)
	return openapi.UpdateIntegrationApp200JSONResponse(response), err
}

func (s strictOpenAPIServer) integrationAppByPublicID(
	ctx context.Context, raw string,
) (integrationstore.IntegrationAppRecord, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return integrationstore.IntegrationAppRecord{}, err
	}
	id, err := publicid.Decode(publicid.KindIntegrationApp, raw)
	if err != nil {
		return integrationstore.IntegrationAppRecord{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	app, err := s.server.store.Integrations().GetIntegrationApp(ctx, org.ID, id)
	if err != nil {
		return app, apierror.OrgScoped(err)
	}
	return app, nil
}

func (s strictOpenAPIServer) integrationAppCredential(
	ctx context.Context, orgID uuid.UUID, raw string,
) (uuid.UUID, error) {
	id, err := publicid.Decode(publicid.KindSecret, raw)
	if err != nil {
		return uuid.Nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid credential_secret_id")
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return uuid.Nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	secret, err := s.server.store.Secrets().GetVisibleSecret(ctx, orgID, id, principal)
	if err != nil {
		return uuid.Nil, apierror.OrgScoped(err)
	}
	if secret.Kind != secretstore.SecretKindIntegrationCredentials {
		return uuid.Nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "integration_credentials secret is required")
	}
	return id, nil
}

func integrationAppConfiguration(provider string, config openapi.IntegrationAppConfiguration) (json.RawMessage, error) {
	clientID := ""
	if config.ClientId != nil {
		clientID = strings.TrimSpace(*config.ClientId)
	}
	switch provider {
	case integrationstore.IntegrationProviderSlack, integrationstore.IntegrationProviderGitHub:
		if clientID == "" || len(clientID) > 512 {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "provider_config.client_id is required")
		}
		config.ClientId = &clientID
	case integrationstore.IntegrationProviderDiscord:
		if config.ClientId != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "Discord uses provider_app_ref as its client ID")
		}
	default:
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "unsupported integration provider")
	}
	return json.Marshal(config)
}

func publicIntegrationApp(app integrationstore.IntegrationAppRecord) (openapi.IntegrationApp, error) {
	response := openapi.IntegrationApp{
		Provider: app.Provider, ProviderAppRef: app.ProviderAppRef, Name: app.DisplayName,
		State: openapi.IntegrationAppState(app.State), CreatedAt: app.CreatedAt, UpdatedAt: app.UpdatedAt,
	}
	var err error
	response.Id, err = publicID(publicid.KindIntegrationApp, app.ID)
	if err != nil {
		return response, err
	}
	response.OrgId, err = publicID(publicid.KindOrganization, app.OrgID)
	if err != nil {
		return response, err
	}
	response.OwnerProjectId, err = idOrNil(publicid.KindProject, app.OwnerProjectID)
	if err != nil {
		return response, err
	}
	response.CredentialSecretId, err = idOrNil(publicid.KindSecret, app.CredentialSecretID)
	if err != nil {
		return response, err
	}
	// Return only the typed public configuration, never arbitrary stored metadata.
	err = json.Unmarshal(app.ProviderConfig, &response.ProviderConfig)
	return response, err
}
