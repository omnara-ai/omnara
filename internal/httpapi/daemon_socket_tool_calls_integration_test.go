//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type daemonProgramToolCallFixture struct {
	server  *Server
	handler http.Handler
	project publicHTTPProject
	base    daemonProcessFixture
	process daemonProcessFixture
	config  uuid.UUID
}

func programToolAgentYAML(exclude string) string {
	return `instruction: Program tool test agent.
model:
  provider_config: openai-prod
  name: http-test
machine_sources:
  - machine_name: program-tools
    cwd: /work
tools:
  run_command:
    permission: {mode: always_allow}
  storage_list: {}
  storage_read:
    permission: {mode: always_ask}
  web_search: {}
programmatic_tool_calls:
  enabled: true
  exclude: [` + exclude + `]
`
}

func newDaemonProgramToolCallFixture(t *testing.T) *daemonProgramToolCallFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()))
	server := mustNewServer(t, store,
		WithDaemonNotifications(newDaemonSocketRouteTestBus(), newDaemonSocketRouteTestPresence(),
			daemonSocketRouteReplicaID),
		WithProgramToolExecutor(tools.Executor{Store: store}))
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "program-tools")
	base := createDaemonProcessFixture(t, ctx, pool, store, project, time.Now(), "program-tools", "run_command")
	sourceYAML := programToolAgentYAML("web_search")
	compiled := compileHTTPAgentYAMLResolved(t, ctx, store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, sourceYAML)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID: project.ProjectUUID, Source: sourceYAML, SourceFormat: "yaml",
		ConfiguredModelID: parseConfiguredModelID(t, compiled), CompiledDefinition: compiled.CanonicalJSON,
		EffectiveDefinitionHash: compiled.Hash,
	})
	require.NoError(t, err)
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: project.ProjectUUID, AgentConfigID: config.ID, LaunchedBy: httpUserPrincipal(project.AdminUserUUID),
	})
	require.NoError(t, err)
	require.Len(t, launch.MachineBindings, 1)
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: project.ProjectUUID, AgentID: launch.Agent.ID,
		Actor:         httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"script"}]`), IdempotencyKey: "program-input",
	})
	require.NoError(t, err)
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, launch.Agent.ID, claim.RuntimeLock.AgentID)
	modelCall := claimNormalModelCallForHTTPTest(t, ctx, store, project.ProjectUUID, launch.Agent.ID,
		claim.RuntimeLock, []uuid.UUID{input.ID}, config.ID, claim.Model.AdmittedInputTurn.Events[0].Sequence)
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, store, project.ProjectUUID, modelCall.Context)
	tool := model.ToolCall{ID: "script", Name: toolcatalog.ToolNameRunCommand,
		Input: json.RawMessage(`{"command":"./script.sh"}`)}
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "program-model-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{tool})})
	require.NoError(t, err)
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: project.ProjectUUID, AgentID: launch.Agent.ID, RuntimeLockID: claim.RuntimeLock.ID,
			ModelCallContextID: modelCall.Context.ID, ProviderResponse: response,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ProviderCallID: tool.ID, Type: toolcatalog.ToolTypeBuiltIn},
			},
		})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	call, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID: project.ProjectUUID, AgentID: launch.Agent.ID, ID: calls[0].ID, RuntimeLockID: claim.RuntimeLock.ID,
	})
	require.NoError(t, err)
	process, err := storagetest.StartProcessForToolCall(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID: project.ProjectUUID, AgentID: launch.Agent.ID, ToolCallID: call.ID, RuntimeLockID: claim.RuntimeLock.ID,
	}, executionstore.CreateProcessInput{AgentMachineBindingID: launch.MachineBindings[0].ID,
		ExecutionSpec: processcmd.ForShell("./script.sh", processcmd.ShellSH, processcmd.IOModePipe)})
	require.NoError(t, err)
	fixture := base
	fixture.AgentUUID, fixture.BindingUUID = launch.Agent.ID, launch.MachineBindings[0].ID
	fixture.ProcessUUID, fixture.ProcessID = process.ID, testPublicID(t, publicid.KindProcess, process.ID)
	fixture.RuntimeLock, fixture.ToolCallUUID, fixture.ToolCall = claim.RuntimeLock, call.ID, call
	return &daemonProgramToolCallFixture{server: server, handler: handler, project: project, base: base, process: fixture,
		config: config.ID}
}

func (f *daemonProgramToolCallFixture) changeConfig(t *testing.T, sourceYAML string) {
	t.Helper()
	ctx, store := t.Context(), f.project.Store
	compiled := compileHTTPAgentYAMLResolved(t, ctx, store, f.project.OrgUUID, f.project.ProjectUUID,
		f.project.AdminUserUUID, sourceYAML)
	changed, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID: f.project.ProjectUUID, Source: sourceYAML, SourceFormat: "yaml",
			ConfiguredModelID: parseConfiguredModelID(t, compiled), CompiledDefinition: compiled.CanonicalJSON,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID: f.process.AgentUUID, ExpectedCurrentConfigID: f.config, ActorType: "user",
		ActorID: f.project.AdminUserUUID, IdempotencyKey: uuid.NewString(),
	})
	require.NoError(t, err)
	f.config = changed.AgentConfig.ID
}

func (f *daemonProgramToolCallFixture) socket() *daemonSocket {
	return newDaemonSocket(f.server, nil, uuid.New(), f.process.OrgUUID, f.process.MachineUUID,
		f.process.RuntimeUUID, f.process.TokenUUID, false)
}

func programToolOffer(processID, key, name, input string) daemonprotocol.ToolCallOffer {
	if key != "" {
		key = uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
	}
	offer := daemonprotocol.ToolCallOffer{ProcessID: processID}
	offer.Key, offer.Name, offer.Input = key, name, json.RawMessage(input)
	return offer
}

func nextToolCallReport(t *testing.T, socket *daemonSocket) daemonprotocol.ToolCallReport {
	t.Helper()
	select {
	case outbound := <-socket.send:
		require.Equal(t, daemonprotocol.MessageToolCallReport, outbound.msg.Type)
		return *outbound.msg.ToolCallReport
	case <-time.After(15 * time.Second):
		t.Fatal("no tool call report")
		return daemonprotocol.ToolCallReport{}
	}
}

func (f *daemonProgramToolCallFixture) offer(t *testing.T, key, name, input string) daemonprotocol.ToolCallReport {
	t.Helper()
	socket := f.socket()
	offer := programToolOffer(f.process.ProcessID, key, name, input)
	require.NoError(t, socket.handleToolCallOffer(t.Context(), daemonprotocol.Message{ToolCallOffer: &offer}))
	report := nextToolCallReport(t, socket)
	socket.toolCallsDone.Wait()
	require.Equal(t, offer.ProcessID, report.ProcessID)
	require.Equal(t, offer.Key, report.Key)
	return report
}

func (f *daemonProgramToolCallFixture) rejected(t *testing.T, key, name, input, code string) {
	t.Helper()
	report := f.offer(t, key, name, input)
	require.Equal(t, daemonprotocol.AckStatusPermanentReject, report.AckStatus, report.Error)
	require.Equal(t, code, report.ErrorCode, report.Error)
	require.Empty(t, report.State)
}

func (f *daemonProgramToolCallFixture) setMachineFlag(t *testing.T, enabled bool) {
	t.Helper()
	machine := requestJSONWithHeaders(t, f.handler, http.MethodPatch,
		"/api/v1/orgs/"+f.project.OrgID+"/machines/"+testPublicID(t, publicid.KindMachine, f.process.MachineUUID),
		`{"programmatic_tool_calls":`+map[bool]string{true: "true", false: "false"}[enabled]+`}`, "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, enabled, machine["programmatic_tool_calls"])
}

func TestDaemonSocketProgramToolCalls(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	ctx := t.Context()
	store := f.project.Store
	execution := store.Execution()
	offered := func() bool {
		t.Helper()
		offers, err := execution.ListDaemonProcessOffers(ctx, executionstore.DaemonWorkInput{
			Authority: f.process.authority(), Limit: 10})
		require.NoError(t, err)
		for _, offer := range offers {
			if offer.Process.ID == f.process.ProcessUUID {
				message := daemonProcessOfferMessage(f.process.ProcessID, offer)
				require.Equal(t, offer.ProgrammaticToolCalls, message.ProcessOffer.ProgrammaticToolCalls)
				return offer.ProgrammaticToolCalls
			}
		}
		t.Fatal("process was not offered")
		return false
	}
	require.False(t, offered(), "a machine without the flag must not offer tool calls")
	f.setMachineFlag(t, true)
	require.True(t, offered())
	for _, process := range []daemonProcessFixture{f.process, f.base} {
		_, found, err := acceptDaemonProcessOfferForTest(ctx, store, process.authority(), process.ProcessUUID)
		require.NoError(t, err)
		require.True(t, found)
	}
	const unavailable, invalid = daemonprotocol.ErrorCodeToolCallUnavailable, daemonprotocol.ErrorCodeValidationFailed

	f.setMachineFlag(t, false)
	f.rejected(t, "k", toolcatalog.ToolNameStorageList, `{}`, unavailable)
	f.setMachineFlag(t, true)
	optedOut := f.socket()
	other := programToolOffer(f.base.ProcessID, "k", toolcatalog.ToolNameStorageList, `{}`)
	optedOut.serveToolCall(ctx, other, nil)
	require.Equal(t, unavailable, nextToolCallReport(t, optedOut).ErrorCode)
	require.Error(t, f.socket().handleToolCallOffer(ctx, daemonprotocol.Message{
		ToolCallOffer: &daemonprotocol.ToolCallOffer{ProcessID: f.process.ProcessID}}), "an offer needs a key")

	notUUID := programToolOffer(f.process.ProcessID, "", toolcatalog.ToolNameStorageList, `{}`)
	notUUID.Key = "not-a-uuid"
	malformed := f.socket()
	malformed.serveToolCall(ctx, notUUID, nil)
	require.Equal(t, invalid, nextToolCallReport(t, malformed).ErrorCode)
	f.rejected(t, "k", toolcatalog.ToolNameStorageList, `[1]`, invalid)
	f.rejected(t, "k", "no_such_tool", `{}`, unavailable)
	f.rejected(t, "k", toolcatalog.ToolNameRunCommand, `{"command":"ls"}`, unavailable)
	f.rejected(t, "k", toolcatalog.ToolNameWebSearch, `{"query":"x"}`, unavailable)
	f.rejected(t, "k", toolcatalog.ToolNameToolSearch, `{"pattern":"("}`, invalid)

	search := f.offer(t, "search", toolcatalog.ToolNameToolSearch, `{"pattern":".*"}`)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, search.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, search.Outcome)
	require.Empty(t, search.ToolCallID)
	var listing struct {
		Pattern string `json:"pattern"`
		Total   int    `json:"total"`
		Tools   []struct {
			Name         string          `json:"name"`
			InputSchema  json.RawMessage `json:"input_schema"`
			OutputSchema json.RawMessage `json:"output_schema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(structuredResult(t, search.ProgramToolCallResult), &listing))
	var names []string
	for _, tool := range listing.Tools {
		require.NotEmpty(t, tool.InputSchema)
		if tool.Name == toolcatalog.ToolNameStorageList {
			require.NotEmpty(t, tool.OutputSchema, "built-in tools carry their output schema")
		}
		names = append(names, tool.Name)
	}
	require.Equal(t, len(names), listing.Total)
	require.Contains(t, names, toolcatalog.ToolNameStorageList)
	require.NotContains(t, names, toolcatalog.ToolNameWebSearch)
	require.NotContains(t, names, toolcatalog.ToolNameRunCommand)

	listed := f.offer(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, listed.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, listed.Outcome)
	require.Contains(t, string(structuredResult(t, listed.ProgramToolCallResult)), "entries")
	require.NotEmpty(t, listed.ToolCallID)
	replay := f.offer(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, listed.ProgramToolCallResult, replay.ProgramToolCallResult)
	f.rejected(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/memory"}`,
		daemonprotocol.ErrorCodeIdempotencyConflict)

	agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
		testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
	parentID := testPublicID(t, publicid.KindToolCall, f.process.ToolCallUUID)
	children := requestJSONWithHeaders(t, f.handler, http.MethodGet,
		agentPath+"/tool-calls?parent_tool_call_id="+parentID, "", "", http.StatusOK, authHeaders(f.project.AdminToken))
	childItems, ok := children["data"].([]any)
	require.True(t, ok)
	require.Len(t, childItems, 1)
	child, ok := childItems[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, listed.ToolCallID, child["id"])
	require.Equal(t, "program", child["caller"])
	require.Equal(t, parentID, child["parent_tool_call_id"])
	require.Equal(t, "completed", child["state"])

	asking := f.socket()
	ask := programToolOffer(f.process.ProcessID, "ask", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	askCtx, stopAsking := context.WithCancel(ctx)
	require.NoError(t, asking.handleToolCallOffer(askCtx, daemonprotocol.Message{ToolCallOffer: &ask}))
	require.NoError(t, asking.handleToolCallOffer(askCtx, daemonprotocol.Message{ToolCallOffer: &ask}),
		"a repeated offer is served once")
	waiting := nextToolCallReport(t, asking)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, waiting.State)
	require.NotEmpty(t, waiting.ToolCallID)
	stopAsking()
	asking.toolCallsDone.Wait()
	require.Empty(t, asking.send, "a call reports its approval wait once")
	reconnected := f.socket()
	require.NoError(t, reconnected.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &ask}))
	require.Equal(t, waiting.ProgramToolCallResult, nextToolCallReport(t, reconnected).ProgramToolCallResult)
	interactions := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	open, ok := interactions["data"].([]any)
	require.True(t, ok)
	require.Len(t, open, 1)
	interaction, ok := open[0].(map[string]any)
	require.True(t, ok)
	interactionID, ok := interaction["id"].(string)
	require.True(t, ok)
	requestJSONWithHeaders(t, f.handler, http.MethodPost,
		agentPath+"/interactions/"+interactionID+"/resolve",
		httpQuestionResolutionRequest(toolpermission.AllowOptionIndex), "", http.StatusOK,
		authHeaders(f.project.AdminToken))
	approved := nextToolCallReport(t, reconnected)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, approved.State)
	require.Equal(t, waiting.ToolCallID, approved.ToolCallID)
	reconnected.toolCallsDone.Wait()

	abandoned := f.socket()
	gone := programToolOffer(f.process.ProcessID, "gone", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	require.NoError(t, abandoned.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &gone}))
	goneWaiting := nextToolCallReport(t, abandoned)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, goneWaiting.State)
	cancel := daemonprotocol.Message{ToolCallCancel: &daemonprotocol.ToolCallCancel{
		ProcessID: gone.ProcessID, Key: gone.Key}}
	require.NoError(t, abandoned.handleToolCallCancel(cancel))
	require.NoError(t, abandoned.handleToolCallCancel(cancel), "a repeated cancel is ignored")
	abandoned.toolCallsDone.Wait()
	require.Empty(t, abandoned.send, "a canceled call reports nothing more")
	canceled := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/tool-calls/"+goneWaiting.ToolCallID,
		"", "", http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, "completed", canceled["state"])
	require.Equal(t, "canceled", canceled["outcome"])
	interactions = requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions?state=open", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	require.Empty(t, interactions["data"], "the canceled call's approval prompt is closed")
	require.NoError(t, abandoned.handleToolCallCancel(cancel), "a cancel for a finished call is ignored")

	busy := f.socket()
	for range cap(busy.send) {
		busy.send <- daemonSocketOutbound{msg: daemonprotocol.Message{Type: daemonprotocol.MessageHeartbeatAck}}
	}
	queued := programToolOffer(f.process.ProcessID, "queued", toolcatalog.ToolNameToolSearch, `{"pattern":"storage"}`)
	require.NoError(t, busy.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &queued}))
	//nolint:omnaralint // nothing signals that the report is waiting on the full queue
	time.Sleep(200 * time.Millisecond)
	for range cap(busy.send) {
		require.Equal(t, daemonprotocol.MessageHeartbeatAck, (<-busy.send).msg.Type)
	}
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, nextToolCallReport(t, busy).State)
	busy.toolCallsDone.Wait()
	select {
	case <-busy.done:
		t.Fatal("a full send queue closed the socket")
	default:
	}

	orphan := func(key string, lease time.Duration) executionstore.ProgramToolCallRef {
		call, err := execution.CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
			ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID, ProcessID: f.process.ProcessUUID,
			Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)), Name: toolcatalog.ToolNameStorageList,
			Type: toolcatalog.ToolTypeBuiltIn, Input: json.RawMessage(`{"pattern":"/artifacts"}`)})
		require.NoError(t, err)
		ref := executionstore.ProgramToolCallRef{ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID,
			ProcessID: f.process.ProcessUUID, ToolCallID: call.ID, ClaimID: uuid.New()}
		_, found, err := execution.ClaimProgramToolCall(ctx, ref, time.Minute)
		require.NoError(t, err)
		require.True(t, found)
		_, err = execution.AuthorizeProgramToolCall(ctx, ref, lease)
		require.NoError(t, err)
		return ref
	}
	releasedRef := orphan("released", time.Minute)
	require.NoError(t, execution.ReleaseProgramToolCallClaim(ctx, releasedRef))
	rerun := f.offer(t, "released", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, testPublicID(t, publicid.KindToolCall, releasedRef.ToolCallID), rerun.ToolCallID)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, rerun.Outcome)
	lostRef := orphan("lost", time.Microsecond)
	rerun = f.offer(t, "lost", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, testPublicID(t, publicid.KindToolCall, lostRef.ToolCallID), rerun.ToolCallID)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, rerun.Outcome)

	stale, err := execution.CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID, ProcessID: f.process.ProcessUUID,
		Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte("stale")), Name: toolcatalog.ToolNameStorageList,
		Type: toolcatalog.ToolTypeBuiltIn, Input: json.RawMessage(`{"pattern":"/artifacts"}`)})
	require.NoError(t, err)
	f.changeConfig(t, programToolAgentYAML("web_search, storage_list"))
	f.rejected(t, "revoked", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, unavailable)
	f.rejected(t, "stale", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, unavailable)
	f.changeConfig(t, programToolAgentYAML("web_search"))
	resumed := f.offer(t, "stale", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, testPublicID(t, publicid.KindToolCall, stale.ID), resumed.ToolCallID)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, resumed.Outcome)
	f.changeConfig(t, programToolAgentYAML("web_search, tool_search"))
	f.rejected(t, "search-excluded", toolcatalog.ToolNameToolSearch, `{"pattern":".*"}`, unavailable)

	f.server.programToolExecutor = nil
	f.rejected(t, "no-executor", toolcatalog.ToolNameStorageList, `{}`, unavailable)
}

func structuredResult(t *testing.T, result daemonprotocol.ProgramToolCallResult) json.RawMessage {
	t.Helper()
	for _, part := range result.Content {
		if part.Type == daemonprotocol.ProgramToolCallContentStructuredData {
			return part.Value
		}
	}
	t.Fatalf("no structured content in %+v", result)
	return nil
}
