package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Server) completeManagedIntegrationOAuth(
	w http.ResponseWriter, r *http.Request, state integrationOAuthState, code string,
) {
	ctx, cancel := context.WithTimeout(r.Context(), integrationOAuthTimeout)
	defer cancel()
	params, err := s.exchangeManagedIntegrationOAuth(ctx, state, code)
	if err != nil {
		logpkg.Error(ctx, fmt.Errorf("complete managed integration setup: %w", err))
		outcome := "connection_failed"
		if errors.Is(err, storeerr.ErrConflict) {
			outcome = "already_connected"
		}
		params = url.Values{"integration_oauth_error": {outcome}}
	}
	s.redirectOAuthOutcome(w, r, state.ReturnTo, params)
}

func (s *Server) exchangeManagedIntegrationOAuth(
	ctx context.Context, state integrationOAuthState, code string,
) (url.Values, error) {
	app, payload, err := s.integrationSetupApp(ctx, state.OrgID, state.ProjectID,
		state.IntegrationAppID, state.AppConfigurationRevision)
	if err != nil {
		return nil, err
	}
	if app.Provider != state.Provider {
		return nil, storeerr.ErrUnauthorized
	}
	redirectURI := s.absolutePublicURL(integrationOAuthCallbackPath)
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: state.OrgID, ProjectID: state.ProjectID, IntegrationAppID: app.ID,
		ExpectedAppConfigurationRevision: app.ConfigurationRevision,
		InstalledBy:                      identitystore.NewUserPrincipal(state.InstalledByUserID),
		Provider:                         app.Provider, IntegrationKind: integrationstore.IntegrationKindManaged,
		State: integrationstore.IntegrationInstallStateActive, OAuthFlowID: state.FlowID,
	}
	switch app.Provider {
	case integrationstore.IntegrationProviderGitHub:
		client, err := s.githubSetupClient(app, payload)
		if err != nil {
			return nil, err
		}
		user, err := client.ExchangeCode(ctx, github.ExchangeInput{
			Code: code, RedirectURI: redirectURI, CodeVerifier: state.CodeVerifier,
		})
		if err != nil {
			return nil, err
		}
		if user.ExpiresAt.Before(state.ExpiresAt) {
			state.ExpiresAt = user.ExpiresAt
		}
		session := integrationSetupSession{State: state, UserToken: user.AccessToken}
		if err := s.saveIntegrationSetupSession(ctx, session); err != nil {
			return nil, err
		}
		flowID, err := publicID(publicid.KindIntegrationOAuthFlow, state.FlowID)
		if err != nil {
			return nil, err
		}
		return url.Values{"integration_oauth": {"select_repository"}, "integration_oauth_flow_id": {flowID}}, nil
	case integrationstore.IntegrationProviderDiscord:
		verified, err := discord.CompleteSetup(ctx, s.discordSetup, discord.SetupCredentials{
			ApplicationID: app.ProviderAppRef, ClientSecret: payload["client_secret"], BotToken: payload["bot_token"],
		}, code, redirectURI)
		if err != nil {
			return nil, err
		}
		input.ConnectionMode = "gateway"
		input.ProviderTenantID, input.ProviderAccountRef = verified.GuildID, verified.BotUserID
		input.DisplayName = verified.GuildName
		// The runtime uses verified bot identity from provider_account_ref. Guild
		// identity is provider_tenant_id; no bot token is duplicated per project.
		input.DiscordRuntimeShardCount = verified.ShardCount
	case integrationstore.IntegrationProviderSlack:
		state.ClientID, err = integrationSetupClientID(app)
		if err != nil {
			return nil, err
		}
		state.ClientSecret, state.SigningSecret = payload["client_secret"], payload["signing_secret"]
		verified, err := s.completeIntegrationOAuth(ctx, state, code, redirectURI)
		if err != nil {
			return nil, err
		}
		if verified.ProviderAccountRef != app.ProviderAppRef {
			return nil, errors.New("slack authorization returned a different app")
		}
		credential, err := s.createSlackIntegrationCredentialSecret(ctx, state.OrgID, state.ProjectID,
			input.InstalledBy, verified.CredentialPayload)
		if err != nil {
			return nil, err
		}
		input.ConnectionMode, input.CredentialSecretID = "webhook", credential.ID
		input.ProviderTenantID, input.ProviderAccountRef = verified.ProviderTenantID, verified.ProviderAccountRef
		input.DisplayName = verified.DisplayName
		input.ProviderIdentity, input.Metadata = verified.ProviderIdentity, verified.Metadata
	default:
		return nil, errors.New("unsupported integration provider")
	}
	install, err := s.saveManagedInstallation(ctx, state, input)
	if err != nil {
		if input.CredentialSecretID != uuid.Nil {
			s.cleanupIntegrationOAuthSecret(ctx, state.OrgID, input.InstalledBy, input.CredentialSecretID)
		}
		return nil, err
	}
	if install.DiscordRuntimeShardCount > 0 && input.DiscordRuntimeShardCount > install.DiscordRuntimeShardCount {
		s.log.WarnContext(ctx, "Discord recommends more shards than the configured runtime; plan an app reshard",
			"integration_app_id", app.ID, "configured_shards", install.DiscordRuntimeShardCount,
			"recommended_shards", input.DiscordRuntimeShardCount)
	}
	return url.Values{"integration_oauth": {"success"}}, nil
}

func (s *Server) saveManagedInstallation(
	ctx context.Context, state integrationOAuthState, input integrationstore.UpsertIntegrationInstallInput,
) (integrationstore.IntegrationInstallRecord, error) {
	if state.AgentProfileID != uuid.Nil {
		_, err := s.store.Integrations().FindIntegrationInstall(ctx,
			state.ProjectID, input.IntegrationAppID, input.ProviderTenantID, input.ProviderAccountRef)
		if errors.Is(err, storeerr.ErrNotFound) {
			behavior := builtInIntegrationBehavior(input.Provider)
			input.InitialRoute = &integrationstore.CreateIntegrationRouteInput{
				AgentProfileID: state.AgentProfileID, DeploymentKey: input.Provider,
				BehaviorKey: behavior,
			}
		} else if err != nil {
			return integrationstore.IntegrationInstallRecord{}, err
		}
	}
	return s.store.Integrations().UpsertIntegrationInstall(ctx, input)
}

func githubInstallationInput(
	state integrationOAuthState, selection github.VerifiedSelection,
) (integrationstore.UpsertIntegrationInstallInput, error) {
	identity, err := json.Marshal(map[string]string{
		"repository_owner":   selection.Repository.Owner.Login,
		"repository_name":    selection.Repository.Name,
		"repository_node_id": selection.Repository.NodeID,
	})
	return integrationstore.UpsertIntegrationInstallInput{
		OrgID: state.OrgID, ProjectID: state.ProjectID, IntegrationAppID: state.IntegrationAppID,
		ExpectedAppConfigurationRevision: state.AppConfigurationRevision,
		InstalledBy:                      identitystore.NewUserPrincipal(state.InstalledByUserID),
		Provider:                         integrationstore.IntegrationProviderGitHub,
		IntegrationKind:                  integrationstore.IntegrationKindManaged,
		ConnectionMode:                   "webhook", State: integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: selection.Installation.ID, ProviderAccountRef: selection.Repository.ID,
		DisplayName: selection.Repository.FullName, ProviderIdentity: identity, OAuthFlowID: state.FlowID,
	}, err
}
