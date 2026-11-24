//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

const customProgramToolsYAML = `  create_ticket:
    type: custom
    description: Create a ticket.
    input_schema: {type: object, properties: {title: {type: string}}, required: [title]}
  close_ticket:
    type: custom
    description: Close a ticket.
    input_schema: {type: object, properties: {id: {type: string}}, required: [id]}
    permission: {mode: always_ask}
  hidden_ticket:
    type: custom
    description: Not for commands.
    input_schema: {type: object}
    code_mode: false
`

func TestDaemonSocketProgramCustomToolCalls(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	f.changeConfig(t, strings.Replace(programToolAgentYAML(true, "storage_list"), "code_mode:\n",
		customProgramToolsYAML+"code_mode:\n", 1))
	ctx := t.Context()
	agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
		testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
	admin := authHeaders(f.project.AdminToken)
	submit := func(toolCallID, body string, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, f.handler, http.MethodPost, agentPath+"/tool-calls/"+toolCallID+"/result",
			body, "", status, admin)
	}

	search := f.offer(t, "search", toolcatalog.ToolNameToolSearch, `{"pattern":"ticket"}`)
	var listing struct {
		Tools []struct {
			Name         string          `json:"name"`
			OutputSchema json.RawMessage `json:"output_schema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(structuredResult(t, search.ProgramToolCallResult), &listing))
	names := map[string]bool{}
	for _, tool := range listing.Tools {
		names[tool.Name] = true
		require.NotEmpty(t, tool.OutputSchema, "%s has an output schema for commands", tool.Name)
	}
	require.Equal(t, map[string]bool{"create_ticket": true, "close_ticket": true}, names)
	f.rejected(t, "hidden", "hidden_ticket", `{}`, daemonprotocol.ErrorCodeToolCallUnavailable)

	malformed := f.offer(t, "malformed", "create_ticket", `{}`)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, malformed.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeFailed, malformed.Outcome,
		"input is checked against the tool's schema before its runner sees it")

	first := f.socket()
	offer := programToolOffer(f.process.ProcessID, "ticket", "create_ticket", `{"title":"Broken build"}`)
	offer.OfferID = "first"
	require.NoError(t, first.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
	waiting := nextToolCallReport(t, first)
	require.Empty(t, waiting.AckStatus, waiting.Error)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingResult, waiting.State)
	require.NotEmpty(t, waiting.ToolCallID)
	cancel := func(socket *daemonSocket, offerID string) {
		t.Helper()
		require.NoError(t, socket.handleToolCallCancel(ctx, daemonprotocol.Message{
			ToolCallCancel: &daemonprotocol.ToolCallCancel{ProcessID: offer.ProcessID, Key: offer.Key, OfferID: offerID}}))
	}
	cancel(first, offer.OfferID)
	first.toolCallsDone.Wait()
	require.True(t, nextToolCallReport(t, first).CancelRecorded,
		"the cancel is acknowledged so the daemon stops sending it")
	stale := f.socket()
	cancel(stale, "earlier")
	stale.toolCallsDone.Wait()
	require.True(t, nextToolCallReport(t, stale).CancelRecorded)

	call := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/tool-calls/"+waiting.ToolCallID,
		"", "", http.StatusOK, admin)
	require.Equal(t, "ready", call["state"], "a command that stops waiting leaves the call pending")
	require.Equal(t, "program", call["caller"])
	require.Equal(t, "custom", call["type"])
	pending := requestJSONWithHeaders(t, f.handler, http.MethodGet, "/api/v1/orgs/"+f.project.OrgID+"/projects/"+
		f.project.ProjectID+"/tool-calls?type=custom&state=ready", "", "", http.StatusOK, admin)
	items, ok := pending["data"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1, "the call's runner finds it among the project's unfinished calls")
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, waiting.ToolCallID, item["id"])

	rerun := f.socket()
	again := offer
	again.OfferID = "rerun"
	require.NoError(t, rerun.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &again}))
	require.Equal(t, waiting.ProgramToolCallResult, nextToolCallReport(t, rerun).ProgramToolCallResult)
	submitted := submit(waiting.ToolCallID,
		`{"outcome":"succeeded","content_blocks":[{"type":"structured_data","value":{"ticket":"T-1"}}]}`,
		http.StatusCreated)
	toolResult, ok := submitted["tool_result"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, toolResult, "event_id", "a command's result is not an agent event")
	require.Equal(t, "succeeded", toolResult["outcome"])
	require.NotEmpty(t, toolResult["created_at"])
	done := nextToolCallReport(t, rerun)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, done.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, done.Outcome)
	require.JSONEq(t, `{"ticket":"T-1"}`, string(structuredResult(t, done.ProgramToolCallResult)))
	rerun.toolCallsDone.Wait()
	replay := f.offer(t, "ticket", "create_ticket", `{"title":"Broken build"}`)
	require.Equal(t, done.ProgramToolCallResult, replay.ProgramToolCallResult)
	submit(waiting.ToolCallID, `{"outcome":"failed","content_blocks":[{"type":"text","text":"late"}]}`,
		http.StatusConflict)
	events := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/events", "", "", http.StatusOK, admin)
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), waiting.ToolCallID, "the call never enters the agent's events")

	asking := f.socket()
	askCtx, disconnect := context.WithCancel(ctx)
	ask := programToolOffer(f.process.ProcessID, "close", "close_ticket", `{"id":"T-1"}`)
	require.NoError(t, asking.handleToolCallOffer(askCtx, daemonprotocol.Message{ToolCallOffer: &ask}))
	approval := nextToolCallReport(t, asking)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, approval.State)
	submit(approval.ToolCallID, `{"outcome":"succeeded","content_blocks":[{"type":"text","text":"early"}]}`,
		http.StatusConflict)
	interactions := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions?state=open", "", "",
		http.StatusOK, admin)
	open, ok := interactions["data"].([]any)
	require.True(t, ok)
	require.Len(t, open, 1)
	interactionID, ok := open[0].(map[string]any)["id"].(string)
	require.True(t, ok)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, agentPath+"/interactions/"+interactionID+"/resolve",
		httpQuestionResolutionRequest(toolpermission.AllowOptionIndex), "", http.StatusOK, admin)
	approved := nextToolCallReport(t, asking)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingResult, approved.State)
	require.Equal(t, approval.ToolCallID, approved.ToolCallID)
	disconnect()
	asking.toolCallsDone.Wait()
	reconnected := f.socket()
	require.NoError(t, reconnected.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &ask}))
	require.Equal(t, approved.ProgramToolCallResult, nextToolCallReport(t, reconnected).ProgramToolCallResult,
		"a lost connection leaves the call waiting for its result")
	submit(approved.ToolCallID, `{"outcome":"failed","content_blocks":[{"type":"text","text":"already closed"}]}`,
		http.StatusCreated)
	failed := nextToolCallReport(t, reconnected)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, failed.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeFailed, failed.Outcome)
	require.Equal(t, "already closed", failed.Content[0].Text)
	reconnected.toolCallsDone.Wait()
}

func TestDaemonSocketProgramCustomToolRevokedBeforeApproval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		mutateYAML  func(string) string
		wantOutcome string
	}{
		{
			name: "code mode disabled",
			mutateYAML: func(base string) string {
				return strings.Replace(base, "code_mode:\n  enabled: true", "code_mode:\n  enabled: false", 1)
			},
			wantOutcome: daemonprotocol.ProgramToolCallOutcomeFailed,
		},
		{
			name: "permission changed to always_deny",
			mutateYAML: func(base string) string {
				return strings.Replace(base, "permission: {mode: always_ask}", "permission: {mode: always_deny}", 1)
			},
			wantOutcome: "denied",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDaemonProgramToolCallFixture(t)
			f.acceptProcess(t)
			initialYAML := strings.Replace(programToolAgentYAML(true, "storage_list"), "code_mode:\n",
				customProgramToolsYAML+"code_mode:\n", 1)
			f.changeConfig(t, initialYAML)
			ctx := t.Context()
			agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
				testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
			admin := authHeaders(f.project.AdminToken)

			socket := f.socket()
			ask := programToolOffer(f.process.ProcessID, "close-revoked", "close_ticket", `{"id":"T-1"}`)
			require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &ask}))
			approval := nextToolCallReport(t, socket)
			require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, approval.State)

			f.changeConfig(t, tc.mutateYAML(initialYAML))

			interactions := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions?state=open",
				"", "", http.StatusOK, admin)
			open, ok := interactions["data"].([]any)
			require.True(t, ok)
			require.Len(t, open, 1)
			interactionID, ok := open[0].(map[string]any)["id"].(string)
			require.True(t, ok)
			requestJSONWithHeaders(t, f.handler, http.MethodPost,
				agentPath+"/interactions/"+interactionID+"/resolve",
				httpQuestionResolutionRequest(toolpermission.AllowOptionIndex), "", http.StatusOK, admin)

			done := nextToolCallReport(t, socket)
			socket.toolCallsDone.Wait()
			require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, done.State)
			require.Equal(t, tc.wantOutcome, done.Outcome)

			pending := requestJSONWithHeaders(t, f.handler, http.MethodGet,
				"/api/v1/orgs/"+f.project.OrgID+"/projects/"+f.project.ProjectID+"/tool-calls?type=custom&state=ready",
				"", "", http.StatusOK, admin)
			items, ok := pending["data"].([]any)
			require.True(t, ok)
			require.Empty(t, items, "revoked custom call must not appear as ready for external runners")
			requestJSONWithHeaders(t, f.handler, http.MethodPost, agentPath+"/tool-calls/"+approval.ToolCallID+"/result",
				`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"late"}]}`, "", http.StatusConflict, admin)
		})
	}
}

func TestDaemonSocketProgramCustomToolScalarResults(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	f.changeConfig(t, strings.Replace(programToolAgentYAML(true, "storage_list"), "code_mode:\n",
		customProgramToolsYAML+"code_mode:\n", 1))
	ctx := t.Context()
	agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
		testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
	admin := authHeaders(f.project.AdminToken)

	for _, tc := range []struct {
		key   string
		value string
	}{
		{key: "scalar-null", value: "null"},
		{key: "scalar-false", value: "false"},
		{key: "scalar-zero", value: "0"},
		{key: "scalar-string", value: `"ok"`},
	} {
		socket := f.socket()
		offer := programToolOffer(f.process.ProcessID, tc.key, "create_ticket", `{"title":"scalar"}`)
		require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
		waiting := nextToolCallReport(t, socket)
		require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingResult, waiting.State)

		requestJSONWithHeaders(t, f.handler, http.MethodPost, agentPath+"/tool-calls/"+waiting.ToolCallID+"/result",
			`{"outcome":"succeeded","content_blocks":[{"type":"structured_data","value":`+tc.value+`}]}`,
			"", http.StatusCreated, admin)
		done := nextToolCallReport(t, socket)
		socket.toolCallsDone.Wait()
		require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, done.State)
		require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, done.Outcome)
		require.Len(t, done.Content, 1)
		require.Equal(t, daemonprotocol.ProgramToolCallContentStructuredData, done.Content[0].Type)
		require.Equal(t, tc.value, string(done.Content[0].Value))
	}
}
