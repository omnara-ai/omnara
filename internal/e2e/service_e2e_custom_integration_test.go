//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestServiceE2ECustomIntegrationWorkerJourney(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "custom-integration-worker")
	const toolName = "post_ticket_update"
	const callID = "call_custom_update"
	const resultText = "Ticket 42 updated"
	const finalText = "The ticket update is complete."
	requests := make(chan map[string]any, 3)
	var requestCount atomic.Int64
	fail := func(w http.ResponseWriter, status int, format string, args ...any) {
		t.Errorf(format, args...)
		http.Error(w, "mock model request failed", status)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer service-e2e-test-key" {
			fail(w, http.StatusBadRequest, "unexpected model request: %s", r.URL.Path)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "decode model request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			requests <- body
			writeOpenAIFunctionCall(w, fail, "resp_custom_tool", callID, toolName,
				map[string]any{"ticket": "42", "text": "An agent is reviewing this ticket."})
		case 2:
			requests <- body
			writeOpenAIMessage(w, fail, "resp_custom_done", finalText)
		case 3:
			requests <- body
			writeOpenAIMessage(w, fail, "resp_customer_reply", "The customer reply has been received.")
		default:
			fail(w, http.StatusInternalServerError, "unexpected extra model request: %s", mustJSONString(body))
		}
	}))
	defer model.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(
		t,
		ctx,
		"custom-integration-worker",
		`instruction: Help with customer support.
model:
  provider_config: openai-prod
  name: service-e2e-local
tools:
  post_ticket_update:
    type: custom
    description: Post an update to the support ticket.
    permission:
      mode: always_ask
    input_schema:
      type: object
      additionalProperties: false
      required: [ticket, text]
      properties:
        ticket: {type: string}
        text: {type: string}
`,
	)
	// The customer service uses ordinary configuration and public APIs; no app
	// registration, provider connection or hosted routing is needed.
	launchBody := map[string]any{
		"config": project.configID, "profile": project.agentID,
		"initial_input": map[string]any{"content_blocks": []any{map[string]any{
			"type": "text", "text": "Please post an update to ticket 42.",
		}}},
	}
	created := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agents",
		launchBody, "ticket-event-42", project.adminToken, http.StatusCreated)
	agent := testutil.RequireType[map[string]any](t, created["agent"])
	agentID := testutil.RequireType[string](t, agent["id"])
	agentPath := project.projectPath + "/agents/" + agentID
	derived := testutil.RequireType[map[string]any](t, created["agent_config"])
	require.Equal(t, project.configID, derived["id"])
	require.Equal(t, derived["id"], agent["current_config_id"])
	require.NotEmpty(t, testutil.RequireType[map[string]any](t, created["agent_input"])["id"])
	replay := env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		project.projectPath+"/agents",
		launchBody,
		"ticket-event-42",
		project.adminToken,
		http.StatusOK,
	)
	require.Equal(t, agentID, testutil.RequireType[map[string]any](t, replay["agent"])["id"])
	profile := env.requestJSON(t, ctx, http.MethodGet, project.projectPath+"/agent-profiles/"+project.agentID,
		nil, "", project.adminToken, http.StatusOK)
	require.Equal(t, project.configID, testutil.RequireType[map[string]any](t, profile["current_config"])["id"])
	worker := env.startWorker(t, ctx, project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: model.URL})

	var interaction map[string]any
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		listed := env.requestJSON(t, ctx, http.MethodGet, agentPath+"/interactions?state=open",
			nil, "", project.adminToken, http.StatusOK)
		items := testutil.RequireType[[]any](t, listed["data"])
		if len(items) != 1 {
			return false, "waiting for worker-created approval; " + worker.logExcerpt()
		}
		interaction = testutil.RequireType[map[string]any](t, items[0])
		return true, ""
	})
	require.Equal(t, "permission", interaction["interaction_kind"])
	require.NotContains(t, interaction, "destination")
	require.NotContains(t, interaction, "presentation_receipt")
	first := <-requests // The worker creates the approval after this model request.
	require.True(t, requestContainsTool(first, toolName), "worker omitted the custom tool")
	interactionID := testutil.RequireType[string](t, interaction["id"])
	resolved := env.requestJSON(t, ctx, http.MethodPost, agentPath+"/interactions/"+interactionID+"/resolve",
		map[string]any{"answers": []any{map[string]any{"option_indices": []int{0}}}},
		"", project.adminToken, http.StatusOK)
	require.Equal(t, "resolved", resolved["state"])
	require.NotContains(t, resolved, "destination")
	var toolID string
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		listed := env.requestJSON(t, ctx, http.MethodGet, agentPath+"/tool-calls?type=custom&state=ready",
			nil, "", project.adminToken, http.StatusOK)
		items := testutil.RequireType[[]any](t, listed["data"])
		if len(items) != 1 {
			return false, "waiting for authorized custom call; " + worker.logExcerpt()
		}
		call := testutil.RequireType[map[string]any](t, items[0])
		require.Equal(t, toolName, call["name"])
		toolID = testutil.RequireType[string](t, call["id"])
		return true, ""
	})
	completed := env.requestJSON(t, ctx, http.MethodPost, agentPath+"/tool-calls/"+toolID+"/result", map[string]any{
		"outcome": "succeeded", "content_blocks": []any{map[string]any{"type": "text", "text": resultText}},
	}, "", project.adminToken, http.StatusCreated)
	require.Equal(t, "completed", testutil.RequireType[map[string]any](t, completed["tool_call"])["state"])
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForAssistantText(t, ctx, env, projectUUID, agentUUID, finalText)
	second := <-requests // Durable final output proves the continuation was requested.
	require.True(t, requestContainsToolResult(second, callID, resultText), "continuation omitted the custom result")
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var locks int
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		return locks == 0, "worker has not settled; " + worker.logExcerpt()
	})
	require.EqualValues(t, 2, requestCount.Load())
	// Customer-owned routing delivers the next event through the ordinary input API.
	followup := map[string]any{
		"content_blocks": []any{map[string]any{"type": "text", "text": "The customer replied: thank you."}},
	}
	next := env.requestJSON(t, ctx, http.MethodPost, agentPath+"/inputs", followup,
		"customer-reply-43", project.adminToken, http.StatusCreated)
	repeated := env.requestJSON(t, ctx, http.MethodPost, agentPath+"/inputs", followup,
		"customer-reply-43", project.adminToken, http.StatusOK)
	require.Equal(t, testutil.RequireType[map[string]any](t, next["agent_input"])["id"],
		testutil.RequireType[map[string]any](t, repeated["agent_input"])["id"])
	waitForAssistantText(t, ctx, env, projectUUID, agentUUID, "The customer reply has been received.")
	third := <-requests
	require.Contains(t, mustJSONString(third), "The customer replied: thank you.")
	require.EqualValues(t, 3, requestCount.Load())
	var connections, apps, listeners int
	require.NoError(t, env.db.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM integration_connections WHERE project_id=$1),
  (SELECT count(*) FROM project_apps WHERE project_id=$1),
  (SELECT count(*) FROM agent_listeners WHERE agent_id=$2)`, projectUUID, agentUUID).
		Scan(&connections, &apps, &listeners))
	require.Zero(t, connections)
	require.Zero(t, apps)
	require.Zero(t, listeners)
}
