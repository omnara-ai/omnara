//go:build integration

package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubHTTPSetupRejectsUnverifiedIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"wrong App", http.StatusOK, `{"id":321,"slug":"helper"}`, http.StatusBadRequest},
		{"provider unavailable", http.StatusServiceUnavailable, "private provider body", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/app", r.URL.Path, "identity failure must stop setup")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(provider.Close)
			handler := newIntegrationServer(openIntegrationDB(t, t.Context()), WithGitHubClientConfig(github.Config{
				APIURL: provider.URL, HTTPClient: provider.Client(),
			}))
			project := bootstrapPublicHTTPProject(t, handler, "github-invalid-identity")
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			secretID := createConnectionHTTPSecret(t, handler, project, "github-credentials", map[string]any{
				"kind": "github_app_credentials", "app_id": "123", "webhook_secret": githubJourneyWebhookSecret,
				"private_key": string(pem.EncodeToMemory(&pem.Block{
					Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
				})),
			})
			body := connectionHTTPBody("github", "123", "456")
			body["credential_secret_id"] = secretID
			response := requestJSONWithHeaders(t, handler, http.MethodPost,
				project.ProjectPath+"/integration-connections", projectAppHTTPJSON(t, body),
				"", tc.want, authHeaders(project.AdminToken))
			require.NotContains(t, projectAppHTTPJSON(t, response), "private provider body")
			var count int
			require.NoError(t, integrationPoolForHandler(t, handler).QueryRow(t.Context(),
				`SELECT count(*) FROM integration_connections WHERE project_id=$1`, project.ProjectUUID).Scan(&count))
			require.Zero(t, count, "public setup must not save an unverified connection")
		})
	}
}

// GitHub setup is real HTTP against this local provider fixture. Public secret
// and connection endpoints still validate and persist all identity observations.
func githubSetupTestConfig(t *testing.T, appSlug ...func() string) github.Config {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := "helper"
		if len(appSlug) > 0 {
			slug = appSlug[0]()
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/installation/token" {
			assert.Equal(t, "Bearer setup-metadata-token", r.Header.Get("Authorization"))
			assert.Equal(t, http.MethodDelete, r.Method)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/users/"+slug+"[bot]" {
			assert.Equal(t, "Bearer setup-metadata-token", r.Header.Get("Authorization"))
			assert.Equal(t, http.MethodGet, r.Method)
			fmt.Fprintf(w, `{"id":999,"login":%q,"type":"Bot"}`, slug+"[bot]")
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if !assert.Len(t, parts, 3) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
		if !assert.NoError(t, err) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var claims struct {
			Issuer string `json:"iss"`
		}
		if !assert.NoError(t, json.Unmarshal(claimsJSON, &claims)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		assert.Contains(t, []string{"123", "124"}, claims.Issuer)
		if r.URL.Path == "/app" {
			assert.Equal(t, http.MethodGet, r.Method)
			fmt.Fprintf(w, `{"id":%s,"slug":%q,"owner":{"id":888}}`, claims.Issuer, slug)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/app/installations/")
		installationID := strings.TrimSuffix(path, "/access_tokens")
		assert.Contains(t, []string{"456", "457", "458", "459", "789"}, installationID)
		if strings.HasSuffix(path, "/access_tokens") {
			assert.Equal(t, http.MethodPost, r.Method)
			var input map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			assert.Equal(t, map[string]any{"permissions": map[string]any{"metadata": "read"}}, input)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"setup-metadata-token","expires_at":%q}`,
				time.Now().Add(time.Hour).Format(time.RFC3339))
			return
		}
		assert.Equal(t, http.MethodGet, r.Method)
		fmt.Fprintf(w, `{"id":%s,"app_id":%s}`, installationID, claims.Issuer)
	}))
	t.Cleanup(provider.Close)
	return github.Config{APIURL: provider.URL, HTTPClient: provider.Client()}
}

func TestGitHubHTTPPutRefreshesRenamedBotLogin(t *testing.T) {
	t.Parallel()
	var renamed atomic.Bool
	config := githubSetupTestConfig(t, func() string {
		if renamed.Load() {
			return "renamed-helper"
		}
		return "helper"
	})
	f := newGitHubHTTPJourney(t, "github-renamed-app", WithGitHubClientConfig(config))
	raw := []byte(githubHTTPComment(t, 42, 3001, "@renamed-helper please review"))
	event, ok, err := integration.NormalizeGitHubAppEvent(f.connection, raw)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, event.Event.Mentioned)
	renamed.Store(true)
	body := connectionHTTPBody("github", "123", "456")
	body["credential_secret_id"] = f.secretID
	path := f.project.ProjectPath + "/integration-connections/" +
		testPublicID(t, publicid.KindIntegrationConnection, f.connection.ID)
	requestJSONWithHeaders(t, f.handler, http.MethodPut, path, projectAppHTTPJSON(t, body),
		"", http.StatusOK, authHeaders(f.project.AdminToken))
	current, err := f.project.Store.Integrations().GetIntegrationConnection(
		t.Context(), f.project.ProjectUUID, f.connection.ID)
	require.NoError(t, err)
	var identity github.AppIdentity
	require.NoError(t, json.Unmarshal(current.ProviderIdentity, &identity))
	require.Equal(t, github.AppIdentity{
		AppID: 123, InstallationID: 456, BotUserID: 999,
		AppSlug: "renamed-helper", BotLogin: "renamed-helper[bot]",
	}, identity)
	require.Equal(t, f.connection.CredentialSecretID, current.CredentialSecretID)
	require.Equal(t, verifiedConnectionVersion(t, f.connection), verifiedConnectionVersion(t, current))
	event, ok, err = integration.NormalizeGitHubAppEvent(current, raw)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, event.Event.Mentioned, "ordinary PUT must repair mention routing after the App is renamed")
}
