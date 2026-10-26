package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckRuns(t *testing.T) {
	var grants []map[string]string
	var bodies []map[string]any
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/456/access_tokens" {
			var grant struct {
				Permissions map[string]string `json:"permissions"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&grant))
			grants = append(grants, grant.Permissions)
			tokenResponse(w)
			return
		}
		if servePreparedPull(w, r) {
			return
		}
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		bodies = append(bodies, body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == repoPath(testRepository())+"/check-runs":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":55}`)
		case r.Method == http.MethodPatch && r.URL.Path == repoPath(testRepository())+"/check-runs/55":
			fmt.Fprint(w, `{"id":55}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	id, err := client.CreateCheckRun(t.Context(), testScope(), CheckRun{
		Name: "my-app", DetailsURL: "https://omnara.test/agent", Title: "Working", Summary: "working",
	})
	require.NoError(t, err)
	require.Equal(t, int64(55), id)
	require.NoError(t, client.CompleteCheckRun(t.Context(), testScope(), id, CheckRun{
		Conclusion: "action_required", Title: "Waiting for input", Summary: "waiting",
	}))
	_, err = client.CreateCheckRun(t.Context(), testScope(), CheckRun{
		Name: "my-app", Conclusion: "neutral", Title: "Finished", Summary: "done",
	})
	require.NoError(t, err)
	checks := map[string]string{"checks": "write", "metadata": "read"}
	assert.Equal(t, []map[string]string{
		{"pull_requests": "read", "metadata": "read"}, checks, checks, checks,
	}, grants)
	output := func(title, summary string) map[string]any { return map[string]any{"title": title, "summary": summary} }
	assert.Equal(t, []map[string]any{
		{"name": "my-app", "head_sha": "commit", "status": "in_progress", "details_url": "https://omnara.test/agent",
			"output": output("Working", "working")},
		{"conclusion": "action_required", "output": output("Waiting for input", "waiting")},
		{"name": "my-app", "head_sha": "commit", "status": "completed", "conclusion": "neutral",
			"output": output("Finished", "done")},
	}, bodies)
}

func TestCheckRunsRequireChecksPermission(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/456/access_tokens" {
			var grant struct {
				Permissions map[string]string `json:"permissions"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&grant))
			if grant.Permissions["checks"] != "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"message":"The permissions requested are not granted to this installation."}`)
				return
			}
			tokenResponse(w)
			return
		}
		if !servePreparedPull(w, r) {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	_, err := client.CreateCheckRun(t.Context(), testScope(), CheckRun{Name: "my-app"})
	require.ErrorIs(t, err, ErrChecksPermissionRequired)
}
