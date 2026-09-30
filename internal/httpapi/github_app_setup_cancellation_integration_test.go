//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/stretchr/testify/require"
)

func TestGitHubManifestConversionSavesCredentialsAfterBrowserCancellation(t *testing.T) {
	t.Parallel()
	f := newGitHubManifestFixture(t)
	_, token, state := f.start(t)
	browserCtx, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	// The local GitHub server has consumed the one-time code, but has not yet
	// sent the credentials. Disconnecting must cancel neither receipt nor save.
	f.onConvert = disconnect
	request := httptest.NewRequest(http.MethodGet,
		"https://omnara.test"+githubManifestCallbackPath+"?"+url.Values{
			"state": {token}, "code": {strings.Repeat("a", 40)},
		}.Encode(), nil).WithContext(browserCtx)
	request.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: f.project.AdminSession})
	response := performRequest(f.handler, request)
	require.ErrorIs(t, browserCtx.Err(), context.Canceled)
	require.EqualValues(t, 1, f.conversions.Load(), "the one-time conversion must not be retried")
	require.Equal(t, http.StatusFound, response.Code, response.Body.String())
	location, err := url.Parse(response.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "credentials_saved", location.Query().Get("github_setup"))
	require.Empty(t, location.Query().Get("github_setup_error"))
	secretID, err := publicid.Decode(publicid.KindSecret, location.Query().Get("credential_secret_id"))
	require.NoError(t, err)
	secret, err := f.project.Store.Secrets().GetSecret(t.Context(), f.project.OrgUUID, secretID)
	require.NoError(t, err)
	require.Equal(t, "github-123-"+state.FlowID.String(), secret.Name)
	credential, err := f.project.Store.Secrets().ReadProjectAvailableSecretPayload(
		t.Context(), secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			SecretID: secretID, Kind: secrets.KindGitHubAppCredentials,
		},
	)
	require.NoError(t, err)
	require.Equal(t, "123", credential.Payload[secrets.KeyAppID])
	require.Equal(t, f.privateKey, credential.Payload[secrets.KeyPrivateKey])
	require.Equal(t, githubJourneyWebhookSecret, credential.Payload[secrets.KeyWebhookSecret])
	current, err := f.project.Store.Integrations().GetIntegration(
		t.Context(), f.project.ProjectUUID, f.integration.ID,
	)
	require.NoError(t, err)
	require.Equal(t, f.integration, current, "credential recovery must not connect or change the integration")
}
