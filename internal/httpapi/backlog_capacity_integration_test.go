//go:build integration

package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAgentInputBacklogFullHTTP(t *testing.T) {
	handler, _ := newMediaIntegrationHandler(t, t.Context())
	project := bootstrapPublicHTTPProject(t, handler, "backlog-cap")
	launch := launchPublicHTTPAgent(t, handler, project, "backlog-cap", project.AdminToken, http.StatusCreated)
	id := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])
	path := project.ProjectPath + "/agents/" + id + "/inputs"
	body := `{"content_blocks":[{"type":"text","text":"pending"}]}`
	var last map[string]any
	for i := range 50 {
		last = requestJSONWithHeaders(t, handler, http.MethodPost, path, body, fmt.Sprintf("pending-%d", i),
			http.StatusCreated, authHeaders(project.AdminToken))
	}
	rejected := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "overflow",
		http.StatusConflict, authHeaders(project.AdminToken))
	require.Equal(t, storeerr.AgentBacklogFullCode, rejected["code"])
	require.Equal(t, "This agent already has 50 pending messages. "+
		"Wait for messages to be processed or cancel a pending message, then try again.", rejected["error"])
	replay := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "pending-49", http.StatusOK,
		authHeaders(project.AdminToken))
	require.NotNil(t, last["agent_input"])
	require.Equal(t, last["agent_input"], replay["agent_input"])
}
