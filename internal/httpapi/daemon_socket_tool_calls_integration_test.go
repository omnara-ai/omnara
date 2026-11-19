//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	pool    *pgxpool.Pool
}

// programToolAgentYAML configures the named tools. storage_read is deferred and asks for approval;
// the others stay loaded, which does not stop commands from calling them.
func programToolAgentYAML(codeMode bool, enabled ...string) string {
	var tools strings.Builder
	for _, name := range enabled {
		tools.WriteString("  " + name + ":\n")
		if name == toolcatalog.ToolNameStorageRead {
			tools.WriteString("    permission: {mode: always_ask}\n    deferred: true\n")
		} else {
			tools.WriteString("    deferred: false\n")
		}
	}
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
` + tools.String() + `code_mode:
  enabled: ` + strconv.FormatBool(codeMode) + `
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
	sourceYAML := programToolAgentYAML(true, "storage_list", "storage_read")
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
		config: config.ID, pool: pool}
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
	require.Equal(t, daemonprotocol.AckStatusPermanentReject, report.AckStatus, "%+v", report)
	require.Equal(t, code, report.ErrorCode, report.Error)
	require.Empty(t, report.State)
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
	require.True(t, offered())
	for _, process := range []daemonProcessFixture{f.process, f.base} {
		_, found, err := acceptDaemonProcessOfferForTest(ctx, store, process.authority(), process.ProcessUUID)
		require.NoError(t, err)
		require.True(t, found)
	}
	const unavailable, invalid = daemonprotocol.ErrorCodeToolCallUnavailable, daemonprotocol.ErrorCodeValidationFailed

	optedOut := f.socket()
	other := programToolOffer(f.base.ProcessID, "k", toolcatalog.ToolNameStorageList, `{}`)
	optedOut.serveToolCall(ctx, other, newServedToolCall(other))
	require.Equal(t, unavailable, nextToolCallReport(t, optedOut).ErrorCode)
	require.Error(t, f.socket().handleToolCallOffer(ctx, daemonprotocol.Message{
		ToolCallOffer: &daemonprotocol.ToolCallOffer{ProcessID: f.process.ProcessID}}), "an offer needs a key")

	badKey := programToolOffer(f.process.ProcessID, "", toolcatalog.ToolNameStorageList, `{}`)
	badKey.Key = "has a space"
	malformed := f.socket()
	malformed.serveToolCall(ctx, badKey, newServedToolCall(badKey))
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
	require.NoError(t, abandoned.handleToolCallCancel(ctx, cancel))
	require.NoError(t, abandoned.handleToolCallCancel(ctx, cancel), "a repeated cancel is harmless")
	abandoned.toolCallsDone.Wait()
	acknowledged := func(socket *daemonSocket, want int) {
		t.Helper()
		require.Len(t, socket.send, want)
		for range want {
			report := nextToolCallReport(t, socket)
			require.True(t, report.CancelRecorded, "a canceled call reports only that its cancel was recorded")
			require.Empty(t, report.State)
		}
	}
	acknowledged(abandoned, 2)
	canceled := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/tool-calls/"+goneWaiting.ToolCallID,
		"", "", http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, "completed", canceled["state"])
	require.Equal(t, "canceled", canceled["outcome"])
	interactions = requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions?state=open", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	require.Empty(t, interactions["data"], "the canceled call's approval prompt is closed")
	require.NoError(t, abandoned.handleToolCallCancel(ctx, cancel))
	abandoned.toolCallsDone.Wait()
	acknowledged(abandoned, 1) // a cancel for a finished call is acknowledged, so it is not sent again

	retried := f.socket()
	again := programToolOffer(f.process.ProcessID, "again", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	againCancel := daemonprotocol.Message{ToolCallCancel: &daemonprotocol.ToolCallCancel{
		ProcessID: again.ProcessID, Key: again.Key}}
	require.NoError(t, retried.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &again}))
	first := nextToolCallReport(t, retried)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, first.State)
	require.NoError(t, retried.handleToolCallCancel(ctx, againCancel))
	retried.workMu.Lock()
	require.Empty(t, retried.toolCalls, "a canceled call stops answering for its key at once")
	retried.workMu.Unlock()
	require.NoError(t, retried.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &again}),
		"the same key offered right after a cancel is served, not dropped")
	require.True(t, nextToolCallReport(t, retried).CancelRecorded)
	second := nextToolCallReport(t, retried)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, second.State)
	require.NotEqual(t, first.ToolCallID, second.ToolCallID, "the canceled call is not reused")
	require.NoError(t, retried.handleToolCallCancel(ctx, againCancel))
	retried.toolCallsDone.Wait()
	require.Empty(t, retried.toolCalls)
	require.Empty(t, retried.toolCallsCanceling)

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
			Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String(), Name: toolcatalog.ToolNameStorageList,
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

	// A cancel ends a call a server is still running: that server loses its claim, and the call no
	// longer counts against the command.
	heldRef := orphan("held", time.Minute)
	holding := f.socket()
	held := programToolOffer(f.process.ProcessID, "held", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.NoError(t, holding.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &held}))
	require.NoError(t, holding.handleToolCallCancel(ctx, daemonprotocol.Message{
		ToolCallCancel: &daemonprotocol.ToolCallCancel{
			ProcessID: held.ProcessID, Key: held.Key}}))
	holding.toolCallsDone.Wait()
	acknowledged(holding, 1)
	stopped := requestJSONWithHeaders(t, f.handler, http.MethodGet,
		agentPath+"/tool-calls/"+testPublicID(t, publicid.KindToolCall, heldRef.ToolCallID), "", "", http.StatusOK,
		authHeaders(f.project.AdminToken))
	require.Equal(t, []any{"completed", "canceled"}, []any{stopped["state"], stopped["outcome"]})
	require.ErrorIs(t, execution.RenewProgramToolCallClaim(ctx, heldRef, time.Minute),
		storeerr.ErrProgramToolCallClaimLost, "the server running a canceled call stops at its next renewal")

	// A cancel reaches a connection that never served its call, after a reconnect. It still ends the
	// stored call, without the call being offered again.
	elsewhere := f.socket()
	away := programToolOffer(f.process.ProcessID, "away", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	away.OfferID = "away-offer"
	awayCtx, disconnect := context.WithCancel(ctx)
	require.NoError(t, elsewhere.handleToolCallOffer(awayCtx, daemonprotocol.Message{ToolCallOffer: &away}))
	awayWaiting := nextToolCallReport(t, elsewhere)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, awayWaiting.State)
	disconnect()
	elsewhere.toolCallsDone.Wait()
	later := f.socket()
	require.NoError(t, later.handleToolCallCancel(ctx, daemonprotocol.Message{
		ToolCallCancel: &daemonprotocol.ToolCallCancel{
			ProcessID: away.ProcessID, Key: away.Key, OfferID: away.OfferID}}))
	later.toolCallsDone.Wait()
	recorded := nextToolCallReport(t, later)
	require.Equal(t, []any{true, away.OfferID}, []any{recorded.CancelRecorded, recorded.OfferID})
	stopped = requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/tool-calls/"+awayWaiting.ToolCallID,
		"", "", http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, []any{"completed", "canceled"}, []any{stopped["state"], stopped["outcome"]})

	// Fields echoed in a rejection are bounded, so the report always fits a message.
	longName := f.socket()
	long := programToolOffer(f.process.ProcessID, "long", strings.Repeat(`"`, 300_000), `{}`)
	require.NoError(t, longName.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &long}))
	tooLong := nextToolCallReport(t, longName)
	require.Equal(t, invalid, tooLong.ErrorCode)
	require.LessOrEqual(t, len(tooLong.Error), daemonprotocol.ProgramToolCallMaxErrorBytes)
	longName.toolCallsDone.Wait()

	// A connection whose runtime is not online cannot start calls, even for a process of its machine.
	offline := newDaemonSocket(f.server, nil, uuid.New(), f.process.OrgUUID, f.process.MachineUUID,
		uuid.New(), f.process.TokenUUID, false)
	unauthorized := programToolOffer(f.process.ProcessID, "offline", toolcatalog.ToolNameStorageList, `{}`)
	require.NoError(t, offline.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &unauthorized}))
	refused := nextToolCallReport(t, offline)
	require.Equal(t, []any{daemonprotocol.AckStatusPermanentReject, unavailable},
		[]any{refused.AckStatus, refused.ErrorCode})
	offline.toolCallsDone.Wait()

	stale, err := execution.CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID, ProcessID: f.process.ProcessUUID,
		Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte("stale")).String(), Name: toolcatalog.ToolNameStorageList,
		Type: toolcatalog.ToolTypeBuiltIn, Input: json.RawMessage(`{"pattern":"/artifacts"}`)})
	require.NoError(t, err)
	f.changeConfig(t, programToolAgentYAML(false, "storage_list", "storage_read"))
	f.rejected(t, "revoked", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, unavailable)
	f.rejected(t, "stale", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, unavailable)
	f.changeConfig(t, programToolAgentYAML(true, "storage_list", "storage_read"))
	resumed := f.offer(t, "stale", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, testPublicID(t, publicid.KindToolCall, stale.ID), resumed.ToolCallID)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, resumed.Outcome)

	// A tool's own code_mode setting keeps commands from calling it while code mode stays on.
	f.changeConfig(t, strings.Replace(programToolAgentYAML(true, "storage_list", "storage_read"),
		"  storage_list:\n", "  storage_list:\n    code_mode: false\n", 1))
	f.rejected(t, "tool-denied", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, unavailable)
	allowed := f.offer(t, "search-tool-denied", toolcatalog.ToolNameToolSearch, `{"pattern":"storage"}`)
	listing.Tools = nil
	require.NoError(t, json.Unmarshal(structuredResult(t, allowed.ProgramToolCallResult), &listing))
	names = nil
	for _, tool := range listing.Tools {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, toolcatalog.ToolNameStorageRead)
	require.NotContains(t, names, toolcatalog.ToolNameStorageList)

	f.changeConfig(t, programToolAgentYAML(false, "storage_list", "storage_read"))
	f.rejected(t, "search-code-mode-off", toolcatalog.ToolNameToolSearch, `{"pattern":".*"}`, unavailable)

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

func (f *daemonProgramToolCallFixture) acceptProcess(t *testing.T) {
	t.Helper()
	_, found, err := acceptDaemonProcessOfferForTest(t.Context(), f.project.Store, f.process.authority(),
		f.process.ProcessUUID)
	require.NoError(t, err)
	require.True(t, found)
}

// A key can be offered again while its call is still open. Each offer is checked against the
// stored call and answered by its own offer ID, so an offer for a different input gets a conflict
// and never the first call's result.
func TestDaemonSocketProgramToolCallAnswersEachOffer(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	socket := f.socket()
	offer := func(offerID, path string) daemonprotocol.ToolCallOffer {
		t.Helper()
		offer := programToolOffer(f.process.ProcessID, "job", toolcatalog.ToolNameStorageRead, `{"path":"`+path+`"}`)
		offer.OfferID, offer.KeepOnDisconnect = offerID, true
		require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
		return offer
	}
	// The first request stops waiting while its call is held open, here by an approval.
	offer("first", "/memory/engineering/notes.md")
	first := nextToolCallReport(t, socket)
	require.Equal(t, "first", first.OfferID)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, first.State)

	offer("other-input", "/memory/engineering/other.md")
	conflict := nextToolCallReport(t, socket)
	require.Equal(t, "other-input", conflict.OfferID)
	require.Equal(t, daemonprotocol.AckStatusPermanentReject, conflict.AckStatus)
	require.Equal(t, daemonprotocol.ErrorCodeIdempotencyConflict, conflict.ErrorCode)
	require.Empty(t, conflict.State)
	require.Empty(t, conflict.Content)

	offer("rerun", "/memory/engineering/notes.md")
	rerun := nextToolCallReport(t, socket)
	require.Equal(t, "rerun", rerun.OfferID)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, rerun.State)
	require.Equal(t, first.ToolCallID, rerun.ToolCallID, "a rerun picks up the original approval")

	agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
		testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
	interactions := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	open, ok := interactions["data"].([]any)
	require.True(t, ok)
	require.Len(t, open, 1, "the offers share one approval")
	interaction, ok := open[0].(map[string]any)
	require.True(t, ok)
	interactionID, ok := interaction["id"].(string)
	require.True(t, ok)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, agentPath+"/interactions/"+interactionID+"/resolve",
		httpQuestionResolutionRequest(toolpermission.AllowOptionIndex), "", http.StatusOK,
		authHeaders(f.project.AdminToken))
	done := nextToolCallReport(t, socket)
	require.Equal(t, "rerun", done.OfferID, "the result goes to the offer that asked for this call")
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, done.State)
	require.Equal(t, first.ToolCallID, done.ToolCallID)
	socket.toolCallsDone.Wait()
	require.Empty(t, socket.send)

	// The finished call answers the same way: its result for the same input, a conflict otherwise.
	offer("after", "/memory/engineering/notes.md")
	after := nextToolCallReport(t, socket)
	require.Equal(t, []string{"after", first.ToolCallID}, []string{after.OfferID, after.ToolCallID})
	offer("after-other", "/memory/engineering/other.md")
	late := nextToolCallReport(t, socket)
	require.Equal(t, []string{"after-other", daemonprotocol.ErrorCodeIdempotencyConflict},
		[]string{late.OfferID, late.ErrorCode})
	socket.toolCallsDone.Wait()
}

// A canceled call keeps its place in the connection's limit until its goroutine has exited, so
// offering and canceling calls while storage is blocked cannot start more than the limit.
func TestDaemonSocketToolCallLimitCountsCanceledCalls(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	blocked, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocked.Rollback(context.WithoutCancel(ctx)) }()
	_, err = blocked.Exec(ctx, `LOCK TABLE processes IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)

	socket := f.socket()
	const over = 16
	for i := range daemonToolCallLimit + over {
		offer := programToolOffer(f.process.ProcessID, "k-"+strconv.Itoa(i), toolcatalog.ToolNameStorageList, `{}`)
		offer.OfferID = strconv.Itoa(i)
		require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
		if i >= daemonToolCallLimit {
			refused := nextToolCallReport(t, socket)
			require.Equal(t, offer.OfferID, refused.OfferID)
			require.Equal(t, daemonprotocol.ErrorCodeToolCallLimitReached, refused.ErrorCode)
			continue
		}
		require.NoError(t, socket.handleToolCallCancel(ctx, daemonprotocol.Message{
			ToolCallCancel: &daemonprotocol.ToolCallCancel{
				ProcessID: offer.ProcessID, Key: offer.Key, OfferID: offer.OfferID}}))
	}
	socket.workMu.Lock()
	active, serving := socket.toolCallsActive, len(socket.toolCalls)
	socket.workMu.Unlock()
	require.Equal(t, daemonToolCallLimit, active, "canceled calls still blocked in storage count against the limit")
	require.Zero(t, serving)

	require.NoError(t, blocked.Rollback(ctx))
	for range daemonToolCallLimit {
		require.True(t, nextToolCallReport(t, socket).CancelRecorded, "each cancel is acknowledged")
	}
	socket.toolCallsDone.Wait()
	socket.workMu.Lock()
	active = socket.toolCallsActive
	socket.workMu.Unlock()
	require.Zero(t, active)
	room := programToolOffer(f.process.ProcessID, "room", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &room}))
	for {
		report := nextToolCallReport(t, socket)
		if report.Key == room.Key {
			require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, report.State)
			break
		}
	}
	socket.toolCallsDone.Wait()
}

// A call waiting for approval keeps its place when its key is offered again, even after a config
// change that would stop a new call to that tool. It is not left open with nobody serving it: once
// approved it runs under the current config and fails there.
func TestDaemonSocketProgramToolCallReattachesAfterConfigChange(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	offer := programToolOffer(f.process.ProcessID, "pending", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	first := f.socket()
	firstCtx, disconnect := context.WithCancel(ctx)
	require.NoError(t, first.handleToolCallOffer(firstCtx, daemonprotocol.Message{ToolCallOffer: &offer}))
	waiting := nextToolCallReport(t, first)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, waiting.State)
	disconnect()
	first.toolCallsDone.Wait()

	f.changeConfig(t, strings.Replace(programToolAgentYAML(true, "storage_list", "storage_read"),
		"  storage_read:\n", "  storage_read:\n    code_mode: false\n", 1))
	f.rejected(t, "new", toolcatalog.ToolNameStorageRead, `{"path":"/memory/engineering/notes.md"}`,
		daemonprotocol.ErrorCodeToolCallUnavailable)

	reconnected := f.socket()
	require.NoError(t, reconnected.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
	again := nextToolCallReport(t, reconnected)
	require.Empty(t, again.AckStatus, "the open call is picked up, not rejected: %s", again.Error)
	require.Equal(t, waiting.ProgramToolCallResult, again.ProgramToolCallResult)

	agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
		testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
	interactions := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	open, ok := interactions["data"].([]any)
	require.True(t, ok)
	require.Len(t, open, 1)
	interaction, ok := open[0].(map[string]any)
	require.True(t, ok)
	interactionID, ok := interaction["id"].(string)
	require.True(t, ok)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, agentPath+"/interactions/"+interactionID+"/resolve",
		httpQuestionResolutionRequest(toolpermission.AllowOptionIndex), "", http.StatusOK,
		authHeaders(f.project.AdminToken))
	done := nextToolCallReport(t, reconnected)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, done.State)
	require.Equal(t, waiting.ToolCallID, done.ToolCallID)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeFailed, done.Outcome,
		"the tool can no longer be called from commands: %+v", done.Content)
	reconnected.toolCallsDone.Wait()
}
