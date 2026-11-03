package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/authn"
)

func TestLoadAuthClientRateLimits(t *testing.T) {
	keys := []string{
		"OMNARA_AUTH_SIGNUP_CLIENT_LIMIT",
		"OMNARA_AUTH_LOGIN_CLIENT_LIMIT",
		"OMNARA_AUTH_PASSWORD_RESET_CLIENT_LIMIT",
		"OMNARA_AUTH_TOKEN_CLIENT_LIMIT",
		"OMNARA_AUTH_OAUTH_LOGIN_CLIENT_LIMIT",
		"OMNARA_AUTH_OAUTH_LOGIN_CONNECTOR_CLIENT_LIMIT",
		"OMNARA_AUTH_DEVICE_AUTHORIZATION_CLIENT_LIMIT",
		"OMNARA_AUTH_DEVICE_POLL_CLIENT_LIMIT",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	want := authn.ClientRateLimits{
		Signup: 500, Login: 1000, PasswordReset: 500, Token: 5000,
		OAuthLogin: 2000, OAuthLoginConnector: 1000, DeviceAuthorization: 1000, DevicePoll: 30000,
	}
	cfg, err := Load()
	if err != nil || cfg.AuthClientRateLimits != want {
		t.Fatalf("defaults = %+v, %v; want %+v", cfg.AuthClientRateLimits, err, want)
	}
	for i, key := range keys {
		t.Setenv(key, fmt.Sprint(301+i))
	}
	want = authn.ClientRateLimits{
		Signup: 301, Login: 302, PasswordReset: 303, Token: 304,
		OAuthLogin: 305, OAuthLoginConnector: 306, DeviceAuthorization: 307, DevicePoll: 308,
	}
	cfg, err = Load()
	if err != nil || cfg.AuthClientRateLimits != want {
		t.Fatalf("overrides = %+v, %v; want %+v", cfg.AuthClientRateLimits, err, want)
	}
	for _, key := range keys {
		for _, value := range []string{"0", "-1", "invalid"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Setenv(key, value)
				if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("expected error naming %s, got %v", key, err)
				}
			})
		}
	}
}
