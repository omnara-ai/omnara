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

// programToolAgentYAML is an agent config that lets commands call its tools
// except those in exclude, asking a user before storage_read.
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

// newDaemonProgramToolCallFixture creates a run_command process for an agent
// whose config enables programmatic tool calls and excludes web_search, on a
// machine that does not allow them yet.
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

// changeConfig makes sourceYAML the agent's current config.
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

// socket is a daemon socket of the fixture's machine with no connection: its
// reports are read from the send queue.
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

// offer offers a call on a fresh socket and returns the report that ends it.
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

// rejected offers a call that must not be made.
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

	// Authorization: the machine flag, and a process whose agent did not opt in.
	f.setMachineFlag(t, false)
	f.rejected(t, "k", toolcatalog.ToolNameStorageList, `{}`, unavailable)
	f.setMachineFlag(t, true)
	optedOut := f.socket()
	other := programToolOffer(f.base.ProcessID, "k", toolcatalog.ToolNameStorageList, `{}`)
	optedOut.serveToolCall(ctx, other)
	require.Equal(t, unavailable, nextToolCallReport(t, optedOut).ErrorCode)
	require.Error(t, f.socket().handleToolCallOffer(ctx, daemonprotocol.Message{
		ToolCallOffer: &daemonprotocol.ToolCallOffer{ProcessID: f.process.ProcessID}}), "an offer needs a key")

	// Validation and eligibility.
	notUUID := programToolOffer(f.process.ProcessID, "", toolcatalog.ToolNameStorageList, `{}`)
	notUUID.Key = "not-a-uuid"
	malformed := f.socket()
	malformed.serveToolCall(ctx, notUUID)
	require.Equal(t, invalid, nextToolCallReport(t, malformed).ErrorCode)
	f.rejected(t, "k", toolcatalog.ToolNameStorageList, `[1]`, invalid)
	f.rejected(t, "k", "no_such_tool", `{}`, unavailable)
	f.rejected(t, "k", toolcatalog.ToolNameRunCommand, `{"command":"ls"}`, unavailable)
	f.rejected(t, "k", toolcatalog.ToolNameWebSearch, `{"query":"x"}`, unavailable)
	f.rejected(t, "k", toolcatalog.ToolNameToolSearch, `{"pattern":"("}`, invalid)

	// tool_search is answered inline from callable tools only.
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

	// An allowed call runs as it is offered; offering it again reports the same
	// call, and its key cannot name a different one.
	listed := f.offer(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, listed.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, listed.Outcome)
	require.Contains(t, string(structuredResult(t, listed.ProgramToolCallResult)), "entries")
	require.NotEmpty(t, listed.ToolCallID)
	replay := f.offer(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, listed.ProgramToolCallResult, replay.ProgramToolCallResult)
	f.rejected(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/memory"}`,
		daemonprotocol.ErrorCodeIdempotencyConflict)

	// The public tool-call API shows the child with its parent.
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

	// An always_ask call reports its wait once, then runs when a user approves
	// it, on a socket other than the one that offered it first.
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

	// A config change applies to the running process: a tool it excludes is
	// refused when offered, and a call recorded before the change fails when
	// it is picked up.
	stale, err := execution.CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID, ProcessID: f.process.ProcessUUID,
		Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte("stale")), Name: toolcatalog.ToolNameStorageList,
		Type: toolcatalog.ToolTypeBuiltIn, Input: json.RawMessage(`{"pattern":"/artifacts"}`)})
	require.NoError(t, err)
	f.changeConfig(t, programToolAgentYAML("web_search, storage_list"))
	f.rejected(t, "revoked", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, unavailable)
	f.rejected(t, "stale", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, unavailable)
	// Re-allowing the tool applies to the same process too, and picks up the
	// call a replica recorded but never ran.
	f.changeConfig(t, programToolAgentYAML("web_search"))
	resumed := f.offer(t, "stale", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
	require.Equal(t, testPublicID(t, publicid.KindToolCall, stale.ID), resumed.ToolCallID)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, resumed.Outcome)
	// Excluding tool_search turns off discovery itself, not only its results.
	f.changeConfig(t, programToolAgentYAML("web_search, tool_search"))
	f.rejected(t, "search-excluded", toolcatalog.ToolNameToolSearch, `{"pattern":".*"}`, unavailable)

	// Without an executor every offer is refused.
	f.server.programToolExecutor = nil
	f.rejected(t, "no-executor", toolcatalog.ToolNameStorageList, `{}`, unavailable)
}

// structuredResult is the first structured value, which `omnarad call` prints.
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
