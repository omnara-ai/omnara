//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAgentPanelExplicitModelAndReplayScope(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	foreign := bootstrapPublicHTTPProject(t, handler, "foreign-assistant")
	var modelID, foreignModelID uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT configured_model_id FROM project_model_grants WHERE project_id=$1`, project.ProjectUUID).Scan(&modelID))
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT configured_model_id FROM project_model_grants WHERE project_id=$1`, foreign.ProjectUUID,
	).Scan(&foreignModelID))
	headers := project.adminBrowserAuthHeaders()
	path := "/api/v1/orgs/" + project.OrgID + "/assistant-agents"
	requestID := uuid.New()
	body := map[string]any{
		"project_id": project.ProjectID, "request_id": requestID, "expected_revision": 0, "automatic": false,
	}
	create := func(status int) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		return requestJSONWithHeaders(t, handler, http.MethodPost, path, string(encoded), "", status, headers)
	}
	before := assistantCounts(t, pool)
	body["model_id"] = testPublicID(t, publicid.KindConfiguredModel, foreignModelID)
	create(http.StatusConflict)
	require.Equal(t, before, assistantCounts(t, pool))
	body["model_id"] = testPublicID(t, publicid.KindConfiguredModel, modelID)
	_, err := pool.Exec(t.Context(),
		`UPDATE project_model_grants SET supports_tools=false WHERE project_id=$1`, project.ProjectUUID)
	require.NoError(t, err)
	create(http.StatusConflict)
	require.Equal(t, before, assistantCounts(t, pool))
	grants := requestJSONWithHeaders(t, handler, http.MethodGet,
		project.ProjectPath+"/model-grants", "", "", http.StatusOK, headers)
	grant := testutil.RequireType[map[string]any](t, testutil.RequireType[[]any](t, grants["data"])[0])
	require.Equal(t, false, grant["supports_tools"])
	_, err = pool.Exec(t.Context(),
		`UPDATE project_model_grants SET supports_tools=true WHERE project_id=$1`, project.ProjectUUID)
	require.NoError(t, err)
	created := create(http.StatusOK)
	agentID := mustPublicHTTPID(t, publicid.KindAgent, testutil.RequireType[string](t, created["agent_id"]))
	var selectedModel uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT definition.configured_model_id FROM agents agent
        JOIN agent_configs config ON config.id=agent.current_config_id
        JOIN agent_config_definitions definition ON definition.id=config.definition_id
        WHERE agent.id=$1`, agentID).Scan(&selectedModel))
	require.Equal(t, modelID, selectedModel)
	second := requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/projects",
		`{"name":"Another home"}`, "", http.StatusCreated, headers)
	body["project_id"] = second["id"]
	after := assistantCounts(t, pool)
	create(http.StatusConflict)
	require.Equal(t, after, assistantCounts(t, pool))
}

func TestAgentPanelRequiresConfiguredMCPOrigin(t *testing.T) {
	t.Parallel()
	pool, _, project := assistantFixture(t)
	body := assistantBody(t, project, uuid.New(), 0, false)
	path := "/api/v1/orgs/" + project.OrgID + "/assistant-agents"
	headers := project.adminBrowserAuthHeaders()
	before := assistantCounts(t, pool)
	unconfigured := newIntegrationServer(pool)
	requestJSONWithHeaders(t, unconfigured, http.MethodPost, path, body, "", http.StatusServiceUnavailable, headers)
	require.Equal(t, before, assistantCounts(t, pool))
	// A trusted app origin is sufficient; the request host is never used for MCP.
	fallback := newIntegrationServer(pool, WithPublicURL("https://example.com"))
	headers["Origin"] = "https://example.com"
	headers["Cookie"] = httpauth.BrowserSessionHostCookieName + "=" + project.AdminSession +
		"; " + httpauth.CSRFHostCookieName + "=" + project.AdminCSRF
	created := requestJSONWithHeaders(t, fallback, http.MethodPost, path, body, "", http.StatusOK, headers)
	agentID := mustPublicHTTPID(t, publicid.KindAgent, testutil.RequireType[string](t, created["agent_id"]))
	var compiled string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT definition.compiled_definition::text FROM agents agent
        JOIN agent_configs config ON config.id=agent.current_config_id
        JOIN agent_config_definitions definition ON definition.id=config.definition_id
        WHERE agent.id=$1`, agentID).Scan(&compiled))
	require.Contains(t, compiled, "https://example.com/mcp")
}
