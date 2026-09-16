package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func WithDiscordSetup(config discord.SetupConfig) Option {
	return func(s *Server) { s.discordSetup = config }
}

func WithGitHubSetup(config github.Config) Option {
	return func(s *Server) { s.githubSetup = config }
}

func (s strictOpenAPIServer) StartIntegrationConnection(
	ctx context.Context, request openapi.StartIntegrationConnectionRequestObject,
) (openapi.StartIntegrationConnectionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	if s.server.publicURL == "" || s.server.secretKeyWrapper == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "integration oauth is not configured")
	}
	appID, err := publicid.Decode(publicid.KindIntegrationApp, request.Body.IntegrationAppId)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	app, payload, err := s.server.integrationSetupApp(ctx, scope.project.OrgID, scope.project.ID, appID, 0)
	if err != nil {
		return nil, err
	}
	if err := s.server.requireIntegrationGateway(app.Provider); err != nil {
		return nil, err
	}
	principal, _ := principalFromContext(ctx)
	state := integrationOAuthState{
		OrgID: scope.project.OrgID, ProjectID: scope.project.ID, InstalledByUserID: principal.ID,
		IntegrationAppID: app.ID, AppConfigurationRevision: app.ConfigurationRevision,
		Provider: app.Provider, ExpiresAt: time.Now().UTC().Add(integrationOAuthStateTTL),
	}
	if request.Body.ReturnTo != nil {
		state.ReturnTo = httpauth.SafeReturnTo(*request.Body.ReturnTo)
	}
	state.FlowID, err = uuid.NewV7()
	if err != nil {
		return nil, err
	}
	if request.Body.AgentProfileId != nil {
		state.AgentProfileID, err = publicid.Decode(publicid.KindAgentProfile, *request.Body.AgentProfileId)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_profile_id")
		}
		profile, err := s.server.store.Execution().GetAgentProfile(ctx, state.ProjectID, state.AgentProfileID)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		if err := s.server.validateChannelSetupModel(ctx, profile.CurrentConfig); err != nil {
			return nil, err
		}
	}
	if app.Provider == integrationstore.IntegrationProviderGitHub {
		if s.server.integrationSetupRedis == nil {
			return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable,
				"integration setup session storage is unavailable")
		}
		state.CodeVerifier, err = httpauth.RandomURLToken(32)
		if err != nil {
			return nil, err
		}
	}
	token, err := s.server.encodeIntegrationOAuthState(ctx, state)
	if err != nil {
		return nil, err
	}
	authorizeURL, err := s.server.managedIntegrationAuthorizeURL(ctx, state, app, payload, token)
	if err != nil {
		return nil, err
	}
	flowID, err := publicID(publicid.KindIntegrationOAuthFlow, state.FlowID)
	if err != nil {
		return nil, err
	}
	return openapi.StartIntegrationConnection201JSONResponse{
		Provider: openapi.IntegrationAppProvider(app.Provider), FlowId: flowID,
		OauthUrl: authorizeURL, ExpiresAt: state.ExpiresAt,
	}, nil
}

// Setup can use an eligible app's credentials without granting the project user
// access to their plaintext. Re-read after decryption to detect secret rotation.
func (s *Server) integrationSetupApp(
	ctx context.Context, orgID, projectID, appID uuid.UUID, revision int64,
) (integrationstore.IntegrationAppRecord, secrets.Payload, error) {
	app, err := s.store.Integrations().GetIntegrationApp(ctx, orgID, appID)
	if err != nil {
		return app, nil, apierror.ProjectScoped(err)
	}
	if app.State != integrationstore.IntegrationAppStateActive ||
		app.ConnectorKey != channelconnector.BuiltInConnectorKey ||
		(app.OwnerProjectID != uuid.Nil && app.OwnerProjectID != projectID) {
		return app, nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if revision != 0 && app.ConfigurationRevision != revision {
		return app, nil, apierror.ProjectScoped(storeerr.ErrStateTransitionConflict)
	}
	if app.CredentialSecretID == uuid.Nil {
		return app, nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "the app requires credentials before connecting")
	}
	credential, err := s.store.Secrets().GetIntegrationAssociatedSecretPayload(ctx, orgID, appID, app.CredentialSecretID)
	if err != nil {
		return app, nil, apierror.ProjectScoped(err)
	}
	current, err := s.store.Integrations().GetIntegrationApp(ctx, orgID, appID)
	if err != nil {
		return app, nil, apierror.ProjectScoped(err)
	}
	if current.ConfigurationRevision != app.ConfigurationRevision {
		return app, nil, apierror.ProjectScoped(storeerr.ErrStateTransitionConflict)
	}
	if credential.Kind != secretstore.SecretKindIntegrationCredentials {
		return app, nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "the app requires integration_credentials")
	}
	return app, credential.Payload, nil
}

func (s *Server) managedIntegrationAuthorizeURL(
	ctx context.Context, state integrationOAuthState, app integrationstore.IntegrationAppRecord,
	payload secrets.Payload, token string,
) (string, error) {
	redirectURI := s.absolutePublicURL(integrationOAuthCallbackPath)
	switch app.Provider {
	case integrationstore.IntegrationProviderDiscord:
		if payload["client_secret"] == "" || payload["bot_token"] == "" {
			return "", apierror.FromCode(openapi.ErrorCodeInvalidRequest, "Discord requires bot_token and client_secret")
		}
		ctx, cancel := context.WithTimeout(ctx, integrationOAuthTimeout)
		defer cancel()
		if err := discord.VerifyApplication(ctx, s.discordSetup, discord.SetupCredentials{
			ApplicationID: app.ProviderAppRef, BotToken: payload["bot_token"],
		}); err != nil {
			return "", apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		}
		return discord.AuthorizeURL(s.discordSetup, app.ProviderAppRef, redirectURI, token)
	case integrationstore.IntegrationProviderGitHub:
		client, err := s.githubSetupClient(app, payload)
		if err != nil {
			return "", err
		}
		return client.AuthorizeURL(github.AuthorizeInput{
			RedirectURI: redirectURI, State: token, CodeChallenge: identitystore.PKCES256Challenge(state.CodeVerifier),
		})
	case integrationstore.IntegrationProviderSlack:
		if err := validateSlackSetupPublicURL(s.publicURL); err != nil {
			return "", apierror.FromCode(openapi.ErrorCodeServiceUnavailable, err.Error())
		}
		clientID, err := integrationSetupClientID(app)
		if err != nil {
			return "", err
		}
		if payload["client_secret"] == "" || payload["signing_secret"] == "" {
			return "", apierror.FromCode(openapi.ErrorCodeInvalidRequest, "Slack requires client_secret and signing_secret")
		}
		return s.integrationOAuthAuthorizeURL(app.Provider, clientID, redirectURI, token)
	default:
		return "", apierror.FromCode(openapi.ErrorCodeInvalidRequest, "unsupported integration provider")
	}
}

func integrationSetupClientID(app integrationstore.IntegrationAppRecord) (string, error) {
	var config openapi.IntegrationAppConfiguration
	if json.Unmarshal(app.ProviderConfig, &config) != nil || config.ClientId == nil || *config.ClientId == "" {
		return "", apierror.FromCode(openapi.ErrorCodeInvalidRequest, "provider_config.client_id is required")
	}
	return *config.ClientId, nil
}

func (s *Server) githubSetupClient(
	app integrationstore.IntegrationAppRecord, payload secrets.Payload,
) (*github.Client, error) {
	clientID, err := integrationSetupClientID(app)
	if err != nil {
		return nil, err
	}
	config := s.githubSetup
	config.AppID, config.ClientID = app.ProviderAppRef, clientID
	config.ClientSecret, config.PrivateKey = payload["client_secret"], payload["private_key"]
	client, err := github.NewClient(config)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "GitHub app configuration is invalid")
	}
	return client, nil
}
