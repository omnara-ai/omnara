package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmittedReviewsPreserveBodiesStatesAndPagination(t *testing.T) {
	var pages []string
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, pullPath(testRepository(), 42)+"/reviews", r.URL.Path)
		assert.Equal(t, "1", r.URL.Query().Get("per_page"))
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		if page != "3" {
			next := map[string]string{"1": "2", "2": "3"}[page]
			w.Header().Set("Link", fmt.Sprintf(
				`<http://%s/repositories/789/pulls/42/reviews?page=%s&per_page=1>; rel="next"`, r.Host, next,
			))
		}
		switch page {
		case "1":
			fmt.Fprint(w, `[{"id":10,"body":"Please fix this","state":"CHANGES_REQUESTED",`+
				`"user":{"id":4,"login":"reviewer","type":"User"},"commit_id":"abc",`+
				`"html_url":"https://github.com/octo-org/repo/pull/42#pullrequestreview-10","submitted_at":"2026-09-27T10:00:00Z"}]`)
		case "2":
			fmt.Fprint(w, `[{"id":11,"body":"private draft","state":"PENDING","submitted_at":null}]`)
		case "3":
			fmt.Fprint(w, `[{"id":12,"body":"Approved then dismissed","state":"DISMISSED",`+
				`"submitted_at":"2026-09-27T11:00:00Z"}]`)
		default:
			t.Errorf("unexpected page %s", page)
		}
	}))
	first, err := client.ListReviews(t.Context(), testScope(), PageOptions{PerPage: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"1"}, pages, "one requested page per read")
	require.Equal(t, 2, first.NextPage)
	require.Len(t, first.Reviews, 1)
	require.Equal(t, "Please fix this", first.Reviews[0].Body)
	require.Equal(t, "CHANGES_REQUESTED", first.Reviews[0].State)
	require.Equal(t, "reviewer", first.Reviews[0].User.Login)
	require.Equal(t, "abc", first.Reviews[0].CommitID)
	require.NotNil(t, first.Reviews[0].SubmittedAt)
	require.NotEmpty(t, first.Reviews[0].HTMLURL)
	draft, err := client.ListReviews(t.Context(), testScope(), PageOptions{Page: first.NextPage, PerPage: 1})
	require.NoError(t, err)
	require.NotNil(t, draft.Reviews)
	require.Empty(t, draft.Reviews)
	require.Equal(t, 3, draft.NextPage, "filtered pending reviews must not hide later submitted reviews")
	last, err := client.ListReviews(t.Context(), testScope(), PageOptions{Page: draft.NextPage, PerPage: 1})
	require.NoError(t, err)
	require.Len(t, last.Reviews, 1)
	require.Equal(t, "DISMISSED", last.Reviews[0].State)
	require.Zero(t, last.NextPage)
}

func TestReviewsRetainSharedPageValidation(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `[{},{}]`} {
		t.Run(body, func(t *testing.T) {
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			_, err := client.ListReviews(t.Context(), testScope(), PageOptions{PerPage: 1})
			requireAPIError(t, err, InvalidResponse)
		})
	}
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", fmt.Sprintf(
			`<http://%s/repositories/790/pulls/42/reviews?page=2&per_page=30>; rel="next"`, r.Host,
		))
		_ = json.NewEncoder(w).Encode([]Review{})
	}))
	_, err := client.ListReviews(t.Context(), testScope(), PageOptions{})
	requireAPIError(t, err, InvalidResponse)
}
