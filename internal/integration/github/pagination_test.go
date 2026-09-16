package github

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fullRepositoryPage() string {
	repositories := make([]string, pageSize)
	for i := range pageSize {
		repositories[i] = repositoryResponse(fmt.Sprint(i+1), true)
	}
	return strings.Join(repositories, ",")
}

func TestPaginatedOptionsBeyondFirstPage(t *testing.T) {
	t.Parallel()
	firstPage := fullRepositoryPage()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/app":
			respond(w, appResponse())
		case "/api/app/installations/" + testInstallID:
			respond(w, installationResponse())
		case "/api/user/installations":
			if r.URL.Query().Get("page") == "2" {
				respond(w, `{"total_count":101,"installations":[`+installationResponse()+`]}`)
				return
			}
			installations := make([]string, pageSize)
			for i := range pageSize {
				installations[i] = strings.Replace(installationResponse(), testInstallID, fmt.Sprint(i+1), 1)
			}
			respond(w, `{"total_count":101,"installations":[`+strings.Join(installations, ",")+`]}`)
		case "/api/user/installations/" + testInstallID + "/repositories":
			if r.URL.Query().Get("page") == "2" {
				respond(w, `{"total_count":101,"repositories":[`+repositoryResponse(testRepositoryID, true)+`]}`)
				return
			}
			respond(w, `{"total_count":101,"repositories":[`+firstPage+`]}`)
		default:
			t.Errorf("unexpected page route: %s", r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	})
	installations, err := client.ListInstallations(t.Context(), testUser(), 1)
	require.NoError(t, err)
	require.Len(t, installations.Installations, 100)
	require.Equal(t, 2, installations.NextPage)
	installations, err = client.ListInstallations(t.Context(), testUser(), installations.NextPage)
	require.NoError(t, err)
	require.Zero(t, installations.NextPage)
	require.Equal(t, testInstallID, installations.Installations[0].ID)
	repositories, err := client.ListRepositories(t.Context(), testUser(), testInstallID, 1)
	require.NoError(t, err)
	require.Equal(t, 2, repositories.NextPage)
	repositories, err = client.ListRepositories(t.Context(), testUser(), testInstallID, repositories.NextPage)
	require.NoError(t, err)
	require.Zero(t, repositories.NextPage)
	require.Equal(t, testRepositoryID, repositories.Repositories[0].ID)
}

func TestEmptyInstallationsRemainAUsableBootstrapResult(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		respond(w, `{"total_count":0,"installations":[]}`)
	})
	page, err := client.ListInstallations(t.Context(), testUser(), 1)
	require.NoError(t, err)
	require.Empty(t, page.Installations)
	require.NotNil(t, page.Installations)
	require.Zero(t, page.NextPage)
}

func TestInvalidPaginationDoesNotSilentlyTruncate(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"total_count":-1,"installations":[]}`,
		`{"total_count":5,"installations":[]}`,
		`{"total_count":0,"installations":null}`,
		`{"installations":[]}`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { respond(w, body) })
			_, err := client.ListInstallations(t.Context(), testUser(), 1)
			require.ErrorIs(t, err, ErrInvalidResponse)
		})
	}
}
