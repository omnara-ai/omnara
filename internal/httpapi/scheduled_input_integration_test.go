//go:build integration

package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestScheduledAgentInputHTTP(t *testing.T) {
	handler, _ := newMediaIntegrationHandler(t, t.Context())
	project := bootstrapPublicHTTPProject(t, handler, "scheduled-input")
	launch := launchPublicHTTPAgent(t, handler, project, "scheduled-input", project.AdminToken, http.StatusCreated)
	id := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])
	path := project.ProjectPath + "/agents/" + id + "/inputs"
	headers := authHeaders(project.AdminToken)
	scheduledFor := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	body := `{"scheduled_for":"` + scheduledFor.Format(time.RFC3339) +
		`","content_blocks":[{"type":"text","text":"later"}]}`

	created := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "scheduled-1", http.StatusCreated, headers)
	input := testutil.RequireType[map[string]any](t, created["agent_input"])
	require.Equal(t, "scheduled", input["state"])
	require.Equal(t, "queued", input["delivery_mode"])
	got, err := time.Parse(time.RFC3339, testutil.RequireType[string](t, input["scheduled_for"]))
	require.NoError(t, err)
	require.True(t, got.Equal(scheduledFor), "scheduled_for = %s, want %s", got, scheduledFor)

	replay := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "scheduled-1", http.StatusOK, headers)
	require.Equal(t, created["agent_input"], replay["agent_input"])

	backlog := requestJSONWithHeaders(t, handler, http.MethodGet, path+"/backlog", "", "", http.StatusOK, headers)
	listed := testutil.RequireType[[]any](t, backlog["data"])
	require.NotEmpty(t, listed)
	last := testutil.RequireType[map[string]any](t, listed[len(listed)-1])
	require.Equal(t, input["id"], last["id"])
	require.Equal(t, "scheduled", last["state"])
	require.Equal(t, input["scheduled_for"], last["scheduled_for"])

	past := `{"scheduled_for":"2020-01-01T00:00:00Z","content_blocks":[{"type":"text","text":"past"}]}`
	rejected := requestJSONWithHeaders(t, handler, http.MethodPost, path, past, "", http.StatusBadRequest, headers)
	require.Equal(t, "invalid request: scheduled_for must be in the future", rejected["error"])
	tooFar := `{"scheduled_for":"` + time.Now().Add(25*time.Hour).UTC().Format(time.RFC3339) +
		`","content_blocks":[{"type":"text","text":"too far"}]}`
	rejected = requestJSONWithHeaders(t, handler, http.MethodPost, path, tooFar, "", http.StatusBadRequest, headers)
	require.Equal(t, "invalid request: scheduled_for must be at most 24 hours in the future", rejected["error"])
	steering := `{"scheduled_for":"` + scheduledFor.Format(time.RFC3339) +
		`","delivery_mode":"steering","cancel_open_interactions":true,` +
		`"content_blocks":[{"type":"text","text":"steer"}]}`
	rejected = requestJSONWithHeaders(t, handler, http.MethodPost, path, steering, "", http.StatusBadRequest, headers)
	require.Contains(t, rejected["error"], "cancel_open_interactions is not allowed for scheduled inputs")

	cancelPath := path + "/" + testutil.RequireType[string](t, input["id"]) + "/cancel"
	requestJSONWithHeaders(t, handler, http.MethodPost, cancelPath, "", "", http.StatusOK, headers)
	backlog = requestJSONWithHeaders(t, handler, http.MethodGet, path+"/backlog", "", "", http.StatusOK, headers)
	for _, item := range testutil.RequireType[[]any](t, backlog["data"]) {
		require.NotEqual(t, input["id"], testutil.RequireType[map[string]any](t, item)["id"])
	}
}
