//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discordSetupTestConfig(t *testing.T) discord.Config {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bot "))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v10/users/@me":
			if r.Header.Get("Authorization") == "Bot other-customer-token" {
				fmt.Fprint(w, `{"id":"999","bot":true}`)
			} else {
				fmt.Fprint(w, `{"id":"222","bot":true,"username":"helper","global_name":"Helper"}`)
			}
		case "/api/v10/applications/@me":
			fmt.Fprint(w, `{"id":"111"}`)
		default:
			t.Errorf("unexpected Discord request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(provider.Close)
	return discord.Config{APIURL: provider.URL + "/api/v10", HTTPClient: provider.Client()}
}

func TestDiscordHTTPCredentialReplacementCannotChangeIdentity(t *testing.T) {
	t.Parallel()
	f := newAppSetupIdentityFixture(t, "discord", nil)
	foreignSecret := createAppSetupHTTPSecret(t, f.handler, f.project, "foreign-token",
		map[string]any{"kind": "generic", "value": "other-customer-token"})
	f.body["credential_secret_id"] = foreignSecret
	delete(f.body, "provider_account_ref")
	f.update(t, http.StatusBadRequest)
	current := f.current(t)
	require.Equal(t, f.app.CredentialSecretID, current.CredentialSecretID)
	require.Equal(t, f.app.UpdatedAt, current.UpdatedAt)
	require.JSONEq(t, string(f.app.ProviderIdentity), string(current.ProviderIdentity))
	require.JSONEq(t, string(f.app.ProviderMetadata), string(current.ProviderMetadata))
	f.body["provider_account_ref"] = "999"
	f.update(
		t,
		http.StatusBadRequest,
	)
	require.Equal(t, "222", f.current(t).ProviderAccountRef)
}

func TestDiscordHTTPSetupDiscoversBotIdentity(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()),
		WithDiscordClientConfig(discordSetupTestConfig(t)))
	project := bootstrapPublicHTTPProject(t, handler, "discord-discover-bot")
	secretID := createAppSetupHTTPSecret(t, handler, project, "discord-credentials",
		map[string]any{"kind": "generic", "value": "private-discord-token"})
	body := map[string]any{
		"expected_setup_revision": 1,
		"provider_tenant_id":      "111",
		"credential_secret_id":    secretID,
	}
	app := configureDiscordHTTPApp(t, handler, project, body)
	require.Equal(t, "111", app.ProviderTenantID)
	require.Equal(t, "222", app.ProviderAccountRef)
	require.Equal(t, "Helper", app.ProviderAgentDisplayName)
	require.Equal(t, appstore.ProjectAppStateActive, app.State)
}

func TestDiscordHTTPSetupRejectsUnverifiedIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, app, bot string
		botAccount     bool
		status, want   int
	}{
		{"wrong application", "999", "222", true, http.StatusOK, http.StatusBadRequest},
		{"wrong bot", "111", "999", true, http.StatusOK, http.StatusBadRequest},
		{"human token", "111", "222", false, http.StatusOK, http.StatusServiceUnavailable},
		{"invalid token", "111", "222", true, http.StatusUnauthorized, http.StatusBadRequest},
		{"provider unavailable", "111", "222", true, http.StatusServiceUnavailable, http.StatusServiceUnavailable},
		{"rate limited", "111", "222", true, http.StatusTooManyRequests, http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "Bot private-discord-token", r.Header.Get("Authorization"))
					w.WriteHeader(tc.status)
					if tc.status != http.StatusOK {
						fmt.Fprint(w, `{"message":"private-discord-token","retry_after":60}`)
					} else if r.URL.Path == "/api/v10/users/@me" {
						fmt.Fprintf(w, `{"id":%q,"bot":%t}`, tc.bot, tc.botAccount)
					} else {
						fmt.Fprintf(w, `{"id":%q}`, tc.app)
					}
				}),
			)
			t.Cleanup(provider.Close)
			handler := newIntegrationServer(
				openIntegrationDB(t, t.Context()),
				WithDiscordClientConfig(discord.Config{
					APIURL: provider.URL + "/api/v10", HTTPClient: provider.Client(),
				}),
			)
			project := bootstrapPublicHTTPProject(t, handler, "discord-invalid-identity")
			secretID := createAppSetupHTTPSecret(t, handler, project, "discord-credentials",
				map[string]any{"kind": "generic", "value": "private-discord-token"})
			body := appSetupHTTPBody("111", "222")
			if tc.name != "wrong bot" {
				delete(body, "provider_account_ref")
			}
			body["credential_secret_id"] = secretID
			app := createSetupHTTPApp(t, handler, project, "discord", appdefinition.DiscordThread)
			response := requestJSONWithHeaders(t, handler, http.MethodPost,
				appSetupPath(t, project, app), projectAppHTTPJSON(t, body),
				"", tc.want, authHeaders(project.AdminToken))
			require.NotContains(t, projectAppHTTPJSON(t, response), "private-discord-token")
			var count int
			require.NoError(t, integrationPoolForHandler(t, handler).QueryRow(t.Context(),
				`SELECT count(*) FROM project_apps WHERE project_id=$1 AND state='active'`, project.ProjectUUID).
				Scan(&count))
			require.Zero(t, count, "unverified credentials must not activate the app")
		})
	}
}

func TestDiscordHTTPTokenRotationProviderConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		clear bool
	}{
		{name: "omitted preserves saved configuration"},
		{name: "explicit empty object clears saved configuration", clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			f := newAppSetupIdentityFixture(t, "discord", func(context.Context) error {
				calls++
				return nil
			})
			delete(f.body, "provider_account_ref")
			config := map[string]any{"public_key": strings.Repeat("ab", 32)}
			f.body["provider_config"] = config
			f.update(t, http.StatusOK)
			before := f.current(t)
			require.Equal(t, "Helper", before.ProviderAgentDisplayName)
			require.JSONEq(t, projectAppHTTPJSON(t, config), string(before.ProviderConfig))
			require.Equal(t, f.steps, calls, "saving settings reuses the verified credential")

			rotated := requestJSONWithHeaders(t, f.handler, http.MethodPost,
				"/api/v1/orgs/"+f.project.OrgID+"/secrets/"+
					testPublicID(t, publicid.KindSecret, f.app.CredentialSecretID)+"/versions",
				`{"material":{"kind":"generic","value":"rotated-discord-token"}}`,
				"", http.StatusOK, authHeaders(f.project.AdminToken))
			require.Equal(t, float64(2), rotated["current_version_number"])
			wantConfig := projectAppHTTPJSON(t, config)
			delete(f.body, "provider_config")
			if tc.clear {
				f.body["provider_config"] = map[string]any{}
				wantConfig = `{}`
			}
			f.update(t, http.StatusOK)
			after := f.current(t)
			require.Equal(t, "Helper", after.ProviderAgentDisplayName)
			require.Equal(t, before.SetupRevision+1, after.SetupRevision)
			require.JSONEq(t, wantConfig, string(after.ProviderConfig))
			require.Equal(t, 2*f.steps, calls, "rotated token must be verified with the provider")
			secret, err := f.project.Store.Secrets().GetSecret(
				t.Context(), f.project.OrgUUID, f.app.CredentialSecretID)
			require.NoError(t, err)
			require.NotEqual(t, verifiedAppCredentialVersion(t, before), secret.CurrentVersionID)
			require.Equal(t, secret.CurrentVersionID, verifiedAppCredentialVersion(t, after))
			response := requestJSONWithHeaders(t, f.handler, http.MethodGet,
				strings.TrimSuffix(appSetupPath(t, f.project, f.app), "/setup"),
				"", "", http.StatusOK, authHeaders(f.project.AdminToken))
			require.JSONEq(t, wantConfig, projectAppHTTPJSON(t, response["provider_config"]))

			f.body["expected_setup_revision"] = before.SetupRevision
			f.body["provider_config"] = map[string]any{"public_key": strings.Repeat("cd", 32)}
			f.update(t, http.StatusConflict)
			require.Equal(t, after, f.current(t))
			require.Equal(t, 2*f.steps, calls)
		})
	}
}

func configureDiscordHTTPApp(
	t *testing.T, handler http.Handler, project publicHTTPProject, body map[string]any,
) appstore.ProjectAppRecord {
	t.Helper()
	app := createSetupHTTPApp(t, handler, project, "discord", appdefinition.DiscordThread)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		appSetupPath(t, project, app),
		projectAppHTTPJSON(t, body),
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	app, err := project.Store.Apps().
		GetProjectApp(t.Context(), project.ProjectUUID, app.ID)
	require.NoError(t, err)
	var identity discord.Identity
	require.NoError(t, json.Unmarshal(app.ProviderIdentity, &identity))
	require.Equal(t, discord.Identity{ApplicationID: "111", BotUserID: "222"}, identity)
	return app
}
