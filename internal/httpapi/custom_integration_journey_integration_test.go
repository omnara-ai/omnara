//go:build integration

package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestCustomIntegrationPublicInputInteractionAndToolJourney(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"api", "dashboard"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			f := newCustomIntegrationHTTPFixture(t, "custom-journey-"+surface)
			ctx := t.Context()
			token := customIntegrationHTTPKey(t, f.handler, f.project, "router", "operator")
			inputBody := customIntegrationHTTPInput()
			inputBody["actor"] = map[string]any{"provider_tenant_id": "helpdesk", "provider_user_id": "customer-7"}
			body := projectAppHTTPJSON(t, inputBody)
			created := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.path+"/inputs", body,
				"ticket-event-1", http.StatusCreated, authHeaders(token))
			input := testutil.RequireType[map[string]any](t, created["agent_input"])
			inputID := mustPublicHTTPID(t, publicid.KindAgentInput, testutil.RequireType[string](t, input["id"]))
			require.NotEmpty(t, input["actor_id"])
			replay := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.path+"/inputs", body,
				"ticket-event-1", http.StatusOK, authHeaders(token))
			require.Equal(t, input, replay["agent_input"])

			// Only the model/permission boundary uses a deterministic kernel fixture.
			// Setup, launch, input, approval discovery/resolution and results use HTTP.
			interaction, tool := customIntegrationHTTPPermission(t, f, inputID)
			destination, err := interaction.CapturedDestination()
			require.NoError(t, err)
			require.Nil(t, destination)
			interactionID := testPublicID(t, publicid.KindAgentInteraction, interaction.ID)
			toolID := testPublicID(t, publicid.KindToolCall, tool.ID)
			listed := customIntegrationHTTPInteraction(t, f, authHeaders(token))
			require.NotContains(t, listed, "destination")
			require.NotContains(t, listed, "presentation_receipt")
			answers := map[string]any{"answers": []any{map[string]any{"option_indices": []int{0}}}}
			headers := f.project.adminBrowserAuthHeaders()
			if surface == "api" {
				headers = authHeaders(token)
				answers["actor"] = map[string]any{"provider_tenant_id": "helpdesk", "provider_user_id": "approver-9"}
			}
			resolvePath := f.path + "/interactions/" + interactionID + "/resolve"
			resolved := requestJSONWithHeaders(t, f.handler, http.MethodPost, resolvePath,
				projectAppHTTPJSON(t, answers), "", http.StatusOK, headers)
			require.Equal(t, "resolved", resolved["state"])
			require.NotEmpty(t, resolved["resolved_by_input_id"])
			require.Equal(t, resolved, customIntegrationHTTPInteraction(t, f, f.project.adminBrowserAuthHeaders()))
			requestJSONWithHeaders(t, f.handler, http.MethodPost, resolvePath,
				projectAppHTTPJSON(t, answers), "", http.StatusOK, headers)
			requestJSONWithHeaders(
				t,
				f.handler,
				http.MethodPost,
				resolvePath,
				`{"answers":[{"option_indices":[1],"text":"Changed mind"}]}`,
				"",
				http.StatusConflict,
				authHeaders(token),
			)
			ready := requestJSONWithHeaders(t, f.handler, http.MethodGet, f.path+"/tool-calls?type=custom&state=ready",
				"", "", http.StatusOK, authHeaders(token))
			calls := testutil.RequireType[[]any](t, ready["data"])
			require.Len(t, calls, 1)
			require.Equal(t, toolID, testutil.RequireType[map[string]any](t, calls[0])["id"])
			resultPath := f.path + "/tool-calls/" + toolID + "/result"
			resultBody := `{"outcome":"succeeded","content_blocks":[{"type":"text","text":"Ticket updated"}]}`
			completed := requestJSONWithHeaders(t, f.handler, http.MethodPost, resultPath,
				resultBody, "", http.StatusCreated, authHeaders(token))
			require.Equal(t, "completed", testutil.RequireType[map[string]any](t, completed["tool_call"])["state"])
			requestJSONWithHeaders(t, f.handler, http.MethodPost, resultPath,
				resultBody, "", http.StatusConflict, authHeaders(token))
			readback := requestJSONWithHeaders(t, f.handler, http.MethodGet,
				f.path+"/tool-calls?type=custom&state=completed", "", "", http.StatusOK, authHeaders(token))
			completedCalls := testutil.RequireType[[]any](t, readback["data"])
			require.Len(t, completedCalls, 1)
			require.Equal(t, completed["tool_call"], completedCalls[0])
			result := testutil.RequireType[map[string]any](t, completed["tool_result"])
			events := requestJSONWithHeaders(t, f.handler, http.MethodGet, f.path+"/events",
				"", "", http.StatusOK, authHeaders(token))
			var resultEvents []map[string]any
			for _, value := range testutil.RequireType[[]any](t, events["data"]) {
				event := testutil.RequireType[map[string]any](t, value)
				if event["event_kind"] == "tool_result" && event["tool_call_id"] == toolID {
					resultEvents = append(resultEvents, event)
				}
			}
			require.Len(t, resultEvents, 1, "duplicate completion must not append a second result")
			require.Equal(t, "succeeded", resultEvents[0]["outcome"])
			require.Equal(t, result["content_blocks"], resultEvents[0]["content_blocks"])
			require.True(t, publicEventTextEquals(resultEvents[0], "Ticket updated"))
			pool := integrationPoolForHandler(t, f.handler)
			assertInteractionResponseAgentInput(t, ctx, pool, f.project.ProjectUUID, f.agent.ID, interaction.ID)
			assertInteractionResponseLedgerEvent(t, ctx, pool, f.project.ProjectUUID, f.agent.ID, interaction.ID)
			var actorProvider, actorTenant, actorUser string
			require.NoError(
				t,
				pool.QueryRow(ctx, `SELECT actor.provider, actor.provider_tenant_id, actor.provider_user_id
    FROM agent_inputs input JOIN actors actor ON actor.id=input.actor_id WHERE input.id=$1`, inputID).
					Scan(&actorProvider, &actorTenant, &actorUser),
			)
			require.Equal(t, "external", actorProvider)
			require.Equal(t, "helpdesk", actorTenant)
			require.Equal(t, "customer-7", actorUser)
			var apps, listeners int
			require.NoError(t, pool.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM project_apps WHERE project_id=$1),
    (SELECT count(*) FROM agent_listeners WHERE agent_id=$2)`, f.project.ProjectUUID, f.agent.ID).
				Scan(&apps, &listeners))
			require.Zero(t, apps)
			require.Zero(t, listeners)
		})
	}
}

func TestCustomIntegrationPublicInputAuthorizationAndReplay(t *testing.T) {
	t.Parallel()
	f := newCustomIntegrationHTTPFixture(t, "custom-input-auth")
	token := customIntegrationHTTPKey(t, f.handler, f.project, "operator", "operator")
	viewer := customIntegrationHTTPKey(t, f.handler, f.project, "viewer", "viewer")
	body := projectAppHTTPJSON(t, customIntegrationHTTPInput())
	requestJSONWithHeaders(t, f.handler, http.MethodPost, f.path+"/inputs", body, "event", http.StatusUnauthorized, nil)
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path+"/inputs",
		body,
		"event",
		http.StatusForbidden,
		authHeaders(viewer),
	)
	created := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path+"/inputs",
		body,
		"event",
		http.StatusCreated,
		authHeaders(token),
	)
	repeated := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path+"/inputs",
		body,
		"event",
		http.StatusOK,
		authHeaders(token),
	)
	require.Equal(t, created["agent_input"], repeated["agent_input"])
	changed := customIntegrationHTTPInput()
	changed["content_blocks"] = []any{map[string]any{"type": "text", "text": "Different content"}}
	requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		f.path+"/inputs",
		projectAppHTTPJSON(t, changed),
		"event",
		http.StatusConflict,
		authHeaders(token),
	)
}

func TestCustomIntegrationPublicInputRejectsOrigin(t *testing.T) {
	t.Parallel()
	f := newCustomIntegrationHTTPFixture(t, "custom-origin-rejected")
	token := customIntegrationHTTPKey(t, f.handler, f.project, "operator", "operator")
	body := customIntegrationHTTPInput()
	body["origin"] = map[string]any{
		"app_id":  testPublicID(t, publicid.KindProjectApp, uuid.New()),
		"address": map[string]any{"kind": "channel", "ref": "C123"},
	}
	rejected := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.path+"/inputs",
		projectAppHTTPJSON(t, body), "rejected-origin", http.StatusBadRequest, authHeaders(token))
	require.Equal(t, "validation_failed", rejected["code"])
	require.Contains(t, rejected["error"], "origin")
	var count int
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`, f.agent.ID).Scan(&count))
	require.Zero(t, count)
}

func TestCustomIntegrationPublicInputMediaReplay(t *testing.T) {
	t.Parallel()
	handler, _ := newMediaIntegrationHandler(t, t.Context())
	project := bootstrapPublicHTTPProject(t, handler, "custom-media")
	launch := launchPublicHTTPAgent(t, handler, project, "custom-media", project.AdminToken, http.StatusCreated)
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])
	path := project.ProjectPath + "/agents/" + agentID + "/inputs"
	headers := authHeaders(project.AdminToken)
	inputs, artifacts := map[string]bool{}, map[string]bool{}
	for _, event := range []string{"first", "second"} {
		body := projectAppHTTPJSON(t, map[string]any{"content_blocks": []any{map[string]any{
			"type": "media", "media_type": "text/plain", "filename": "ticket.txt",
			"data": base64.StdEncoding.EncodeToString([]byte(event)),
		}}})
		created := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, event, http.StatusCreated, headers)
		input := testutil.RequireType[map[string]any](t, created["agent_input"])
		inputs[testutil.RequireType[string](t, input["id"])] = true
		blocks := testutil.RequireType[[]any](t, input["content_blocks"])
		artifactID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, blocks[0])["artifact_id"])
		artifacts[artifactID] = true
		replay := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, event, http.StatusOK, headers)
		require.Equal(t, input, replay["agent_input"])
	}
	require.Len(t, inputs, 2)
	require.Len(t, artifacts, 2)
}

type customIntegrationHTTPFixture struct {
	handler http.Handler
	project publicHTTPProject
	agent   executionstore.AgentRecord
	path    string
}

func newCustomIntegrationHTTPFixture(t *testing.T, seed string) customIntegrationHTTPFixture {
	t.Helper()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, seed)
	source := map[string]any{
		"instruction": "Help with support tickets.",
		"model":       map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
		"tools": map[string]any{"lookup_ticket": map[string]any{
			"type": "custom", "description": "Look up a support ticket.",
			"permission": map[string]any{"mode": "always_ask"},
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"ticket": map[string]any{"type": "string"}},
				"required": []string{"ticket"}, "additionalProperties": false,
			},
		}},
	}
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		seed,
		"json",
		projectAppHTTPJSON(t, source),
		project.AdminToken,
		http.StatusCreated,
	)
	configID := testutil.RequireType[string](t, config["id"])
	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		seed,
		"Support",
		configID,
		project.AdminToken,
		http.StatusCreated,
	)
	launchBody := projectAppHTTPJSON(t, map[string]any{"profile": profile["id"], "config": configID})
	launched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		launchBody,
		seed,
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	replay := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		launchBody,
		seed,
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	createdAgent := testutil.RequireType[map[string]any](t, launched["agent"])
	replayedAgent := testutil.RequireType[map[string]any](t, replay["agent"])
	for _, field := range []string{"id", "current_config_id", "agent_profile_id", "created_at"} {
		require.Equal(t, createdAgent[field], replayedAgent[field], "replay preserves %s", field)
	}
	agentID := testutil.RequireType[string](t, createdAgent["id"])
	agent, err := project.Store.Execution().
		GetAgentInProject(t.Context(), project.ProjectUUID, mustPublicHTTPID(t, publicid.KindAgent, agentID))
	require.NoError(t, err)
	return customIntegrationHTTPFixture{handler, project, agent, project.ProjectPath + "/agents/" + agentID}
}

func customIntegrationHTTPInput() map[string]any {
	return map[string]any{"content_blocks": []any{map[string]any{"type": "text", "text": "Please review this ticket"}}}
}

func customIntegrationHTTPInteraction(
	t *testing.T,
	f customIntegrationHTTPFixture,
	headers map[string]string,
) map[string]any {
	t.Helper()
	listed := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodGet,
		f.path+"/interactions",
		"",
		"",
		http.StatusOK,
		headers,
	)
	data := testutil.RequireType[[]any](t, listed["data"])
	require.Len(t, data, 1)
	return testutil.RequireType[map[string]any](t, data[0])
}

func customIntegrationHTTPKey(t *testing.T, handler http.Handler, project publicHTTPProject, name, role string) string {
	t.Helper()
	path := "/api/v1/orgs/" + project.OrgID + "/api-keys"
	created := requestJSONWithHeaders(t, handler, http.MethodPost, path,
		projectAppHTTPJSON(t, map[string]any{"name": name, "org_role": "member"}),
		"", http.StatusCreated, project.adminBrowserAuthHeaders())
	id := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, created["api_key"])["id"])
	requestJSONWithHeaders(t, handler, http.MethodPut, path+"/"+id+"/projects/"+project.ProjectID,
		projectAppHTTPJSON(t, map[string]any{"role": role}), "", http.StatusOK, project.adminBrowserAuthHeaders())
	return testutil.RequireType[string](t, created["token"])
}

func customIntegrationHTTPPermission(
	t *testing.T, f customIntegrationHTTPFixture, inputID uuid.UUID,
) (executionstore.AgentInteractionRecord, executionstore.ToolCallRecord) {
	t.Helper()
	ctx := t.Context()
	claim, found, err := f.project.Store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
	require.Len(t, claim.Model.AdmittedInputTurn.Inputs, 1)
	require.Equal(t, inputID, claim.Model.AdmittedInputTurn.Inputs[0].ID)
	sequence := claim.Model.AdmittedInputTurn.Events[0].Sequence
	snapshot, err := f.project.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx, f.project.ProjectUUID, f.agent.ID, sequence)
	require.NoError(t, err)
	modelCall := claimNormalModelCallForHTTPTest(t, ctx, f.project.Store, f.project.ProjectUUID, f.agent.ID,
		claim.RuntimeLock, []uuid.UUID{inputID}, snapshot.AgentConfig.ID, sequence)
	toolInput := json.RawMessage(`{"ticket":"42"}`)
	response, err := model.NewResponseEnvelopeForStorage(
		"gpt-test", modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault,
		model.Response{
			ID: "response-ticket", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{ID: "call-ticket", Name: "lookup_ticket", Input: toolInput},
			}),
		})
	require.NoError(t, err)
	_, calls, err := f.project.Store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: f.project.ProjectUUID, AgentID: f.agent.ID, RuntimeLockID: claim.RuntimeLock.ID,
			ModelCallContextID: modelCall.Context.ID, ProviderResponse: response,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ProviderCallID: "call-ticket", Type: toolcatalog.ToolTypeCustom},
			},
		})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	permission, err := toolpermission.ParseRequest(httpPermissionRequest(t, "lookup_ticket", toolInput))
	require.NoError(t, err)
	interaction, err := f.project.Store.Execution().CreatePermissionInteraction(ctx,
		executionstore.CreatePermissionInteractionInput{
			ProjectID: f.project.ProjectUUID, AgentID: f.agent.ID, ToolCallID: calls[0].ID,
			RuntimeLockID: claim.RuntimeLock.ID, Request: permission,
		})
	require.NoError(t, err)
	return interaction, calls[0]
}
