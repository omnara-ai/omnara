//go:build integration

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestAppSetupNameConflictAndMalformedID(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	conflict := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/apps",
		`{"name":"slack","definition_id":"omnara.slack","settings":{}}`, "", http.StatusConflict,
		authHeaders(f.project.AdminToken))
	require.Equal(t, "conflict", conflict["code"])
	require.Equal(t, `conflict: an app named "slack" already exists in this project; choose a different name`,
		conflict["error"])
	f.assertOneApp(t)

	// The nil UUID passes the route's string pattern but fails public ID parsing.
	invalidID := "app_" + strings.Repeat("a", 26)
	for _, manifest := range []bool{false, true} {
		path, body := projectSlackSetupRequest(t, f.project, invalidID, manifest)
		response := requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", http.StatusBadRequest,
			authHeaders(f.project.AdminToken))
		require.Equal(t, "invalid_request", response["code"])
	}
	for _, suffix := range []string{"/setup", "/disconnect"} {
		body := ""
		if suffix == "/setup" {
			body = projectAppHTTPJSON(t, map[string]any{
				"expected_setup_revision": 1,
				"credential_secret_id":    testPublicID(t, publicid.KindSecret, f.app.ID),
				"provider_tenant_id":      "T123", "provider_account_ref": "A123",
			})
		}
		response := requestJSONWithHeaders(t, f.handler, http.MethodPost,
			f.project.ProjectPath+"/apps/"+invalidID+suffix, body, "", http.StatusBadRequest,
			authHeaders(f.project.AdminToken))
		require.Equal(t, "invalid_request", response["code"])
	}
	require.Zero(t, f.exchanges.Load())
	require.Zero(t, f.manifests.Load())
}

func TestAppOAuthDeletedCallbackUsesOnlyValidatedSealedReturnTo(t *testing.T) {
	t.Parallel()
	f := newProjectSlackOAuthFixture(t, nil)
	token, _ := f.start(t, false)
	require.NoError(t, f.project.Store.Integrations().DeleteProjectApp(t.Context(),
		f.project.OrgUUID, f.project.ProjectUUID, f.app.ID))
	other := bootstrapPublicHTTPProject(t, f.handler, "other-oauth-user")
	for _, tc := range []struct {
		name, token, session string
		status               int
	}{
		{"valid", token, f.project.AdminSession, http.StatusFound},
		{"tampered", token + "tampered", f.project.AdminSession, http.StatusUnauthorized},
		{"wrong user", token, other.AdminSession, http.StatusForbidden},
		{"anonymous", token, "", http.StatusUnauthorized},
	} {
		query := url.Values{
			"state": {tc.token}, "code": {"unused"}, "return_to": {"https://attacker.test/steal"},
		}
		req := httptest.NewRequest(http.MethodGet,
			"https://omnara.test"+integrationOAuthCallbackPath+"?"+query.Encode(), nil)
		if tc.session != "" {
			req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: tc.session})
		}
		rec := performRequest(f.handler, req)
		require.Equal(t, tc.status, rec.Code, "%s: %s", tc.name, rec.Body.String())
		if tc.status == http.StatusFound {
			f.assertFailure(t, rec, "app_deleted")
		} else {
			require.Empty(t, rec.Header().Get("Location"), "%s must not redirect", tc.name)
		}
	}
	require.Zero(t, f.exchanges.Load())
	require.Zero(t, countSlackOAuthCredentialSecrets(t, t.Context(), f.project.Store, f.project))
}
