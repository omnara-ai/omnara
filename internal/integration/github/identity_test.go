package github

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnsupportedRuntimeIDsAreNeverRoundedOrPersistable(t *testing.T) {
	t.Parallel()
	const unsupported = "9007199254740993"
	var raw nativeID
	require.NoError(t, json.Unmarshal([]byte(unsupported), &raw))
	require.Equal(t, unsupported, string(raw))
	config := newTestConfig(t)
	config.AppID = unsupported
	_, err := NewClient(config)
	require.ErrorIs(t, err, ErrUnsupportedID)
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("unsupported selection made a native call")
	})
	_, err = client.VerifySelection(t.Context(), testUser(), unsupported, testRepositoryID, "acme/private-repo")
	require.ErrorIs(t, err, ErrUnsupportedID)
	_, err = client.VerifySelection(t.Context(), testUser(), testInstallID, unsupported, "acme/private-repo")
	require.ErrorIs(t, err, ErrUnsupportedID)
	_, err = client.ListRepositories(t.Context(), testUser(), unsupported, 1)
	require.ErrorIs(t, err, ErrUnsupportedID)

	var repository nativeRepository
	require.NoError(t, json.Unmarshal([]byte(repositoryResponse(unsupported, true)), &repository))
	_, err = repository.project()
	require.ErrorIs(t, err, ErrUnsupportedID)
	var installation nativeInstallation
	body := strings.Replace(installationResponse(), testInstallID, unsupported, 1)
	require.NoError(t, json.Unmarshal([]byte(body), &installation))
	_, err = client.installation(installation)
	require.ErrorIs(t, err, ErrUnsupportedID)
}

func TestRepositoryFactsMustFitRuntimeIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		from string
		to   string
	}{
		{"unsafe owner", "acme", "acme_org"},
		{"invalid name", "private-repo", "private/repo"},
		{"invalid node ID", "R_private", "R private"},
		{"oversized node ID", "R_private", strings.Repeat("R", 513)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := strings.ReplaceAll(repositoryResponse(testRepositoryID, true), tc.from, tc.to)
			var repository nativeRepository
			require.NoError(t, json.Unmarshal([]byte(body), &repository))
			_, err := repository.project()
			require.ErrorIs(t, err, ErrInvalidResponse)
		})
	}
}

func TestOAuthFirstThenRefreshObservesFreshInstallation(t *testing.T) {
	t.Parallel()
	// The same unexpired user token is reused; the client must not cache an empty
	// installation list or treat an install-browser return as connection proof.
	var installed atomic.Bool
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testUser().AccessToken {
			t.Error("refresh changed user authority")
		}
		if !installed.Swap(true) {
			respond(w, `{"total_count":0,"installations":[]}`)
			return
		}
		respond(w, `{"total_count":1,"installations":[`+installationResponse()+`]}`)
	})
	user := testUser()
	page, err := client.ListInstallations(t.Context(), user, 1)
	require.NoError(t, err)
	require.Empty(t, page.Installations)
	page, err = client.ListInstallations(t.Context(), user, 1)
	require.NoError(t, err)
	require.Len(t, page.Installations, 1)
	require.Equal(t, testInstallID, page.Installations[0].ID)
}

func TestInvalidPKCEAndSelectionInputsMakeNoRequests(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input reached native API") })
	_, err := client.AuthorizeURL(AuthorizeInput{
		RedirectURI: "https://omnara.test/callback", State: "state", CodeChallenge: "invalid",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
	_, err = client.ExchangeCode(t.Context(), ExchangeInput{
		Code: "code", RedirectURI: "https://omnara.test/callback", CodeVerifier: "too-short",
	})
	require.ErrorIs(t, err, ErrInvalidInput)
	_, err = client.ListInstallations(t.Context(), testUser(), 0)
	require.ErrorIs(t, err, ErrInvalidInput)
	_, err = client.VerifySelection(t.Context(), testUser(), "../7", testRepositoryID, "acme/private-repo")
	require.ErrorIs(t, err, ErrInvalidInput)
}
