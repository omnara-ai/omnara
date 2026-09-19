//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
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
				fmt.Fprint(w, `{"id":"222","bot":true}`)
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
	f := newConnectionIdentityFixture(t, "discord", nil)
	foreignSecret := createConnectionHTTPSecret(t, f.handler, f.project, "foreign-token",
		map[string]any{"kind": "generic", "value": "other-customer-token"})
	f.body["credential_secret_id"] = foreignSecret
	f.update(t, http.StatusBadRequest)
	current := f.current(t)
	require.Equal(t, f.connection.CredentialSecretID, current.CredentialSecretID)
	require.Equal(t, f.connection.UpdatedAt, current.UpdatedAt)
	require.JSONEq(t, string(f.connection.ProviderIdentity), string(current.ProviderIdentity))
	require.JSONEq(t, string(f.connection.ProviderMetadata), string(current.ProviderMetadata))
	f.body["provider_account_ref"] = "999"
	f.update(t, http.StatusBadRequest) // Even verified replacement IDs cannot change this connection's account.
	require.Equal(t, "222", f.current(t).ProviderAccountRef)
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
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bot private-discord-token", r.Header.Get("Authorization"))
				w.WriteHeader(tc.status)
				if tc.status != http.StatusOK {
					fmt.Fprint(w, `{"message":"private-discord-token","retry_after":60}`)
				} else if r.URL.Path == "/api/v10/users/@me" {
					fmt.Fprintf(w, `{"id":%q,"bot":%t}`, tc.bot, tc.botAccount)
				} else {
					fmt.Fprintf(w, `{"id":%q}`, tc.app)
				}
			}))
			t.Cleanup(provider.Close)
			handler := newIntegrationServer(openIntegrationDB(t, t.Context()), WithDiscordClientConfig(discord.Config{
				APIURL: provider.URL + "/api/v10", HTTPClient: provider.Client(),
			}))
			project := bootstrapPublicHTTPProject(t, handler, "discord-invalid-identity")
			secretID := createConnectionHTTPSecret(t, handler, project, "discord-credentials",
				map[string]any{"kind": "generic", "value": "private-discord-token"})
			body := connectionHTTPBody("discord", "111", "222")
			body["credential_secret_id"] = secretID
			response := requestJSONWithHeaders(t, handler, http.MethodPost,
				project.ProjectPath+"/integration-connections", projectAppHTTPJSON(t, body),
				"", tc.want, authHeaders(project.AdminToken))
			require.NotContains(t, projectAppHTTPJSON(t, response), "private-discord-token")
			var count int
			require.NoError(t, integrationPoolForHandler(t, handler).QueryRow(t.Context(),
				`SELECT count(*) FROM integration_connections WHERE project_id=$1`, project.ProjectUUID).Scan(&count))
			require.Zero(t, count, "unverified credentials must not reserve the globally unique bot identity")
		})
	}
}

func discordHTTPConnection(
	t *testing.T, handler http.Handler, project publicHTTPProject, body map[string]any,
) integrationstore.IntegrationConnectionRecord {
	t.Helper()
	created := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integration-connections",
		projectAppHTTPJSON(t, body), "", http.StatusCreated, authHeaders(project.AdminToken))
	id := mustPublicHTTPID(t, publicid.KindIntegrationConnection, testutil.RequireType[string](t, created["id"]))
	connection, err := project.Store.Integrations().GetIntegrationConnection(t.Context(), project.ProjectUUID, id)
	require.NoError(t, err)
	var identity discord.Identity
	require.NoError(t, json.Unmarshal(connection.ProviderIdentity, &identity))
	require.Equal(t, discord.Identity{ApplicationID: "111", BotUserID: "222"}, identity)
	return connection
}
