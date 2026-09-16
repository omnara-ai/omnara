package github

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectionLocatorIsOnlyAnUntrustedHint(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid hint reached GitHub") })
	for _, name := range []string{
		"", "owner", "owner/repo/extra", "owner/..", "owner/.", "../repo",
		"https://foreign.test/repo", "acme/repo?token=secret", "acme/repo%2fextra", "acme/\x00repo",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := client.VerifySelection(t.Context(), testUser(), testInstallID, testRepositoryID, name)
			require.ErrorIs(t, err, ErrInvalidInput)
		})
	}
}

func TestSelectionRenameRequiresRefreshingPicker(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusMovedPermanently, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/api/app" {
					respond(w, appResponse())
					return
				}
				if r.URL.Path != "/api/repos/acme/private-repo" {
					t.Errorf("followed a stale repository lookup: %s", r.URL.Path)
				}
				if status != http.StatusOK {
					w.Header().Set("Location", "/api/repos/acme/renamed")
					w.WriteHeader(status)
					return
				}
				respond(w, strings.ReplaceAll(repositoryResponse(testRepositoryID, true), "private-repo", "renamed"))
			})
			_, err := client.VerifySelection(t.Context(), testUser(), testInstallID, testRepositoryID, "acme/private-repo")
			require.ErrorIs(t, err, ErrSelectionNotAccessible)
			require.EqualValues(t, 2, calls.Load())
		})
	}
}

func TestRepositoryInstallationProbeUsesAppAuthority(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/api/app":
			respond(w, appResponse())
		case "/api/repos/Acme/Private-Repo":
			if r.Header.Get("Authorization") != "Bearer "+testUser().AccessToken {
				t.Error("repository role lookup used the wrong principal")
			}
			respond(w, repositoryResponse(testRepositoryID, true))
		case "/api/repos/Acme/Private-Repo/installation":
			jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if strings.Count(jwt, ".") != 2 || strings.HasPrefix(jwt, "ghu_") {
				t.Error("installation membership lookup requires an App JWT")
			}
			respond(w, installationResponse())
		default:
			t.Errorf("selection must not enumerate repositories: %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	selection, err := client.VerifySelection(
		t.Context(), testUser(), testInstallID, testRepositoryID, "Acme/Private-Repo",
	)
	require.NoError(t, err)
	require.Equal(t, "acme/private-repo", selection.Repository.FullName)
	require.EqualValues(t, 3, calls.Load())
}
