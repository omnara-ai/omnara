package slack

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/omnara-ai/omnara/internal/secrets"
)

const (
	IntegrationKindAgentProfile = "agent_profile"
	ConnectionModeWebhook       = "webhook"
)

type InstallIdentity struct {
	BotUserID string `json:"bot_user_id"`
}

type InstallMetadata struct {
	TeamName string `json:"team_name,omitempty"`
}

type AppCredentials struct {
	BotToken      string
	ClientID      string
	ClientSecret  string
	SigningSecret string
}

func MarshalInstallIdentity(identity InstallIdentity) (json.RawMessage, error) {
	if strings.TrimSpace(identity.BotUserID) == "" {
		return nil, errors.New("slack bot user id is required")
	}
	return json.Marshal(identity)
}

func ParseInstallIdentity(raw json.RawMessage) (InstallIdentity, error) {
	var identity InstallIdentity
	if err := json.Unmarshal(raw, &identity); err != nil {
		return InstallIdentity{}, err
	}
	if strings.TrimSpace(identity.BotUserID) == "" {
		return InstallIdentity{}, errors.New("slack install identity is incomplete")
	}
	return identity, nil
}

func CredentialPayload(credentials AppCredentials) (secrets.Payload, error) {
	if err := validateAppCredentials(credentials); err != nil {
		return nil, err
	}
	return secrets.Payload{
		secrets.KeyAccessToken:   credentials.BotToken,
		secrets.KeyClientID:      credentials.ClientID,
		secrets.KeyClientSecret:  credentials.ClientSecret,
		secrets.KeySigningSecret: credentials.SigningSecret,
	}, nil
}

func AppCredentialsFromPayload(payload secrets.Payload) (AppCredentials, error) {
	if _, err := secrets.ValidatePayload(secrets.KindSlackAppCredentials, payload); err != nil {
		return AppCredentials{}, err
	}
	credentials := AppCredentials{
		BotToken:      payload[secrets.KeyAccessToken],
		ClientID:      payload[secrets.KeyClientID],
		ClientSecret:  payload[secrets.KeyClientSecret],
		SigningSecret: payload[secrets.KeySigningSecret],
	}
	if err := validateAppCredentials(credentials); err != nil {
		return AppCredentials{}, err
	}
	return credentials, nil
}

func validateAppCredentials(credentials AppCredentials) error {
	if strings.TrimSpace(credentials.BotToken) == "" || strings.TrimSpace(credentials.ClientID) == "" ||
		strings.TrimSpace(credentials.ClientSecret) == "" ||
		strings.TrimSpace(credentials.SigningSecret) == "" {
		return errors.New("slack bot token, client ID, client secret, and signing secret are required")
	}
	return nil
}
