package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

var errIntegrationOAuthMissingScope = errors.New("integration oauth missing required scope")

type SlackOAuthConfig = slack.OAuthConfig

type integrationOAuthProviderInstall struct {
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	CredentialPayload        secrets.Payload
}

func supportedIntegrationOAuthProvider(provider string) bool {
	switch provider {
	case integrationstore.IntegrationProviderSlack:
		return true
	default:
		return false
	}
}

func (s *Server) integrationOAuthAuthorizeURL(
	provider, clientID, redirectURI, stateToken string,
) (string, error) {
	switch provider {
	case integrationstore.IntegrationProviderSlack:
		out, err := slack.AuthorizeURL(s.slackOAuth, clientID, redirectURI, stateToken)
		if errors.Is(err, slack.ErrStateTooLarge) {
			return "", errIntegrationOAuthStateTooLarge
		}
		return out, err
	default:
		return "", errors.New("unsupported integration provider")
	}
}

func (s *Server) completeIntegrationOAuth(
	ctx context.Context,
	state integrationOAuthState,
	code, redirectURI string,
) (integrationOAuthProviderInstall, error) {
	switch state.Provider {
	case integrationstore.IntegrationProviderSlack:
		install, err := slack.CompleteOAuth(
			ctx,
			s.slackOAuth,
			state.ClientID,
			state.ClientSecret,
			code,
			redirectURI,
		)
		if errors.Is(err, slack.ErrMissingScope) {
			return integrationOAuthProviderInstall{}, errIntegrationOAuthMissingScope
		}
		if err != nil {
			return integrationOAuthProviderInstall{}, err
		}
		displayName := strings.TrimSpace(state.BotDisplayName)
		if displayName == "" {
			if name, result, lookupErr := slack.LookupUserDisplayName(
				ctx,
				s.slackOAuth,
				install.AccessToken,
				install.BotUserID,
			); lookupErr == nil && result == (slack.APIResult{}) {
				displayName = name
			}
		}
		identity, err := slack.MarshalInstallIdentity(slack.InstallIdentity{
			BotUserID: install.BotUserID,
		})
		if err != nil {
			return integrationOAuthProviderInstall{}, err
		}
		metadataJSON, err := json.Marshal(slack.InstallMetadata{
			TeamName: install.TeamName,
		})
		if err != nil {
			return integrationOAuthProviderInstall{}, err
		}
		credentialPayload, err := slack.CredentialPayload(slack.AppCredentials{
			BotToken:      install.AccessToken,
			ClientID:      state.ClientID,
			ClientSecret:  state.ClientSecret,
			SigningSecret: state.SigningSecret,
		})
		if err != nil {
			return integrationOAuthProviderInstall{}, err
		}
		return integrationOAuthProviderInstall{
			ProviderTenantID:         install.TenantID,
			ProviderAccountRef:       install.AppID,
			ProviderAgentDisplayName: displayName,
			ProviderIdentity:         identity,
			ProviderMetadata:         metadataJSON,
			CredentialPayload:        credentialPayload,
		}, nil
	default:
		return integrationOAuthProviderInstall{}, errors.New("unsupported integration provider")
	}
}
