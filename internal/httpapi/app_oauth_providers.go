package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/omnara-ai/omnara/internal/apps/slack"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
)

var errAppOAuthMissingScope = errors.New("app oauth missing required scope")

type SlackOAuthConfig = slack.OAuthConfig

type appOAuthProviderInstall struct {
	ProviderTenantID         string
	ProviderAccountRef       string
	ProviderAgentDisplayName string
	ProviderIdentity         json.RawMessage
	ProviderMetadata         json.RawMessage
	CredentialPayload        secrets.Payload
}

func supportedAppOAuthProvider(provider string) bool {
	switch provider {
	case appstore.AppProviderSlack:
		return true
	default:
		return false
	}
}

func (s *Server) appOAuthAuthorizeURL(
	provider, clientID, redirectURI, stateToken string,
) (string, error) {
	switch provider {
	case appstore.AppProviderSlack:
		out, err := slack.AuthorizeURL(s.slackOAuth, clientID, redirectURI, stateToken)
		if errors.Is(err, slack.ErrStateTooLarge) {
			return "", errAppOAuthStateTooLarge
		}
		return out, err
	default:
		return "", errors.New("unsupported app provider")
	}
}

func (s *Server) completeAppOAuth(
	ctx context.Context,
	state appOAuthState,
	code, redirectURI string,
) (appOAuthProviderInstall, error) {
	switch state.Provider {
	case appstore.AppProviderSlack:
		install, err := slack.CompleteOAuth(
			ctx,
			s.slackOAuth,
			state.ClientID,
			state.ClientSecret,
			code,
			redirectURI,
		)
		if errors.Is(err, slack.ErrMissingScope) {
			return appOAuthProviderInstall{}, errAppOAuthMissingScope
		}
		if err != nil {
			return appOAuthProviderInstall{}, err
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
			return appOAuthProviderInstall{}, err
		}
		metadataJSON, err := json.Marshal(slack.InstallMetadata{
			TeamName: install.TeamName,
		})
		if err != nil {
			return appOAuthProviderInstall{}, err
		}
		credentialPayload, err := slack.CredentialPayload(slack.AppCredentials{
			BotToken:      install.AccessToken,
			ClientID:      state.ClientID,
			ClientSecret:  state.ClientSecret,
			SigningSecret: state.SigningSecret,
		})
		if err != nil {
			return appOAuthProviderInstall{}, err
		}
		return appOAuthProviderInstall{
			ProviderTenantID:         install.TenantID,
			ProviderAccountRef:       install.AppID,
			ProviderAgentDisplayName: displayName,
			ProviderIdentity:         identity,
			ProviderMetadata:         metadataJSON,
			CredentialPayload:        credentialPayload,
		}, nil
	default:
		return appOAuthProviderInstall{}, errors.New("unsupported app provider")
	}
}
