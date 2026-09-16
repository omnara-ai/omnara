package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDiscordSetupBindsTheAuthenticatedGuildAndActualBot(t *testing.T) {
	t.Parallel()
	config, credentials, exchanges := setupFixture(t, nil)
	result, err := CompleteSetup(t.Context(), config, credentials, "one-use-code", "https://omnara.test/callback")
	require.NoError(t, err)
	require.Equal(t, VerifiedInstall{
		ApplicationID: "111", BotUserID: "222", GuildID: "333", GuildName: "Customer server", ShardCount: 2,
	}, result)
	require.EqualValues(t, 1, exchanges.Load())
	link, err := AuthorizeURL(config, credentials.ApplicationID, "https://omnara.test/callback", "bound-state")
	require.NoError(t, err)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "bound-state", parsed.Query().Get("state"))
	require.Equal(t, "code", parsed.Query().Get("response_type"))
	require.Equal(t, "bot", parsed.Query().Get("scope"), "setup does not request access to the user's other servers")
	require.NotContains(t, link, credentials.ClientSecret)
	require.NotContains(t, link, credentials.BotToken)
}

func TestDiscordSetupRejectsUnverifiedProviderFacts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, path, body string
		beforeExchange   bool
	}{
		{"different app", "/applications/@me", `{"id":"999","flags":524288,"bot_require_code_grant":true}`, true},
		{"code grant disabled", "/applications/@me", `{"id":"111","flags":524288}`, true},
		{"missing content intent", "/applications/@me", `{"id":"111","flags":0,"bot_require_code_grant":true}`, true},
		{"nonbot token", "/users/@me", `{"id":"222","bot":false}`, true},
		{"missing guild proof", "/oauth/token", `{"token_type":"Bearer","scope":"bot"}`, false},
		{"wrong scope", "/oauth/token", `{"token_type":"Bearer","scope":"identify","guild":{"id":"333"}}`, false},
		{"wrong guild readback", "/guilds/333", `{"id":"444","name":"Wrong server"}`, false},
		{"invalid shards", "/gateway/bot", `{"shards":0}`, false},
		{"duplicate fields", "/gateway/bot", `{"shards":2,"shards":3}`, false},
		{
			"oversize response", "/gateway/bot",
			`{"shards":2,"padding":"` + strings.Repeat("x", setupResponseBytes) + `"}`, false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config, credentials, exchanges := setupFixture(t, map[string]string{test.path: test.body})
			_, err := CompleteSetup(t.Context(), config, credentials, "one-use-code", "https://omnara.test/callback")
			require.Error(t, err)
			require.NotContains(t, err.Error(), credentials.BotToken)
			require.NotContains(t, err.Error(), credentials.ClientSecret)
			if test.beforeExchange {
				require.Zero(t, exchanges.Load(), "bad app configuration must not consume the authorization code")
			} else {
				require.EqualValues(t, 1, exchanges.Load(), "an OAuth code is never retried automatically")
			}
		})
	}
}

func TestDiscordSetupDoesNotFollowRedirectsOrIgnoreCancellation(t *testing.T) {
	t.Parallel()
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(destination.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	config := SetupConfig{APIURL: server.URL, HTTPClient: server.Client()}
	credentials := SetupCredentials{ApplicationID: "111", ClientSecret: "private-secret", BotToken: "private-token"}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := CompleteSetup(ctx, config, credentials, "code", "https://omnara.test/callback")
	require.ErrorContains(t, err, "HTTP 307")
	require.Zero(t, redirected.Load())
	cancel()
	_, err = CompleteSetup(ctx, config, credentials, "code", "https://omnara.test/callback")
	require.ErrorIs(t, err, context.Canceled)
}

func setupFixture(t *testing.T, replacements map[string]string) (SetupConfig, SetupCredentials, *atomic.Int32) {
	t.Helper()
	responses := map[string]string{
		"/applications/@me": `{"id":"111","flags":524288,"bot_require_code_grant":true}`,
		"/users/@me":        `{"id":"222","bot":true}`,
		"/oauth/token":      `{"token_type":"Bearer","scope":"bot","guild":{"id":"333"}}`,
		"/guilds/333":       `{"id":"333","name":"Customer server"}`,
		"/gateway/bot":      `{"shards":2}`,
	}
	for path, body := range replacements {
		responses[path] = body
	}
	var exchanges atomic.Int32
	credentials := SetupCredentials{ApplicationID: "111", ClientSecret: "private-secret", BotToken: "private-token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			exchanges.Add(1)
			if r.Method != http.MethodPost || r.ParseForm() != nil ||
				r.Form.Get("client_secret") != credentials.ClientSecret || r.Form.Get("client_id") != credentials.ApplicationID ||
				r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "one-use-code" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		} else if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bot "+credentials.BotToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, exists := responses[r.URL.Path]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return SetupConfig{APIURL: server.URL, TokenURL: server.URL + "/oauth/token", HTTPClient: server.Client()},
		credentials, &exchanges
}

func TestDiscordSetupPreflightDoesNotExchangeAuthorization(t *testing.T) {
	t.Parallel()
	config, credentials, exchanges := setupFixture(t, nil)
	require.NoError(t, VerifyApplication(t.Context(), config, credentials))
	require.Zero(t, exchanges.Load())
	config, credentials, exchanges = setupFixture(t, map[string]string{
		"/applications/@me": `{"id":"111","flags":524288,"bot_require_code_grant":false}`,
	})
	require.ErrorContains(t, VerifyApplication(t.Context(), config, credentials), "Code Grant")
	require.Zero(t, exchanges.Load())
}
