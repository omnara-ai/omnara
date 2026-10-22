package config

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/authn"
)

func loadAuthClientRateLimits() (authn.ClientRateLimits, error) {
	limits := (authn.ClientRateLimits{}).WithDefaults()
	for _, setting := range []struct {
		key   string
		value *int
	}{
		{"OMNARA_AUTH_SIGNUP_CLIENT_LIMIT", &limits.Signup},
		{"OMNARA_AUTH_LOGIN_CLIENT_LIMIT", &limits.Login},
		{"OMNARA_AUTH_PASSWORD_RESET_CLIENT_LIMIT", &limits.PasswordReset},
		{"OMNARA_AUTH_TOKEN_CLIENT_LIMIT", &limits.Token},
		{"OMNARA_AUTH_OAUTH_LOGIN_CLIENT_LIMIT", &limits.OAuthLogin},
		{"OMNARA_AUTH_OAUTH_LOGIN_CONNECTOR_CLIENT_LIMIT", &limits.OAuthLoginConnector},
		{"OMNARA_AUTH_DEVICE_AUTHORIZATION_CLIENT_LIMIT", &limits.DeviceAuthorization},
		{"OMNARA_AUTH_DEVICE_POLL_CLIENT_LIMIT", &limits.DevicePoll},
	} {
		value, err := getenvInt(setting.key, *setting.value)
		if err != nil {
			return authn.ClientRateLimits{}, err
		}
		if value <= 0 {
			return authn.ClientRateLimits{}, fmt.Errorf("%s must be positive", setting.key)
		}
		*setting.value = value
	}
	return limits, nil
}
