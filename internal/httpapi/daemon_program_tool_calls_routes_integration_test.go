//go:build integration

package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type daemonProgramToolCallFixture struct {
	handler http.Handler
	project publicHTTPProject
	base    daemonProcessFixture
	process daemonProcessFixture
	config  uuid.UUID
}

// programToolAgentYAML is an agent config that lets commands call its tools
// except those in exclude.
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
	handler := newIntegrationServerWithStoreOptions(pool, nil)
	project := bootstrapPublicHTTPProject(t, handler, "program-tools")
	store := integrationStoreForHandler(t, handler)
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
	return &daemonProgramToolCallFixture{handler: handler, project: project, base: base, process: fixture,
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

func (f *daemonProgramToolCallFixture) do(
	t *testing.T, method, path, token, body string, status int,
) daemonprotocol.ProgramToolCallResult {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "/api/v1/daemon/processes/"+f.process.ProcessID+path, reader).
		WithContext(t.Context())
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	require.Equal(t, status, w.Code, w.Body.String())
	var result daemonprotocol.ProgramToolCallResult
	if status == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	}
	return result
}

func (f *daemonProgramToolCallFixture) submit(
	t *testing.T, key, name, input string, status int,
) daemonprotocol.ProgramToolCallResult {
	t.Helper()
	if key != "" {
		key = uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
	}
	body, err := json.Marshal(map[string]any{"key": key, "name": name, "input": json.RawMessage(input)})
	require.NoError(t, err)
	return f.do(t, http.MethodPost, "/tool-calls", f.process.Token, string(body), status)
}

func (f *daemonProgramToolCallFixture) setMachineFlag(t *testing.T, enabled bool) {
	t.Helper()
	machine := requestJSONWithHeaders(t, f.handler, http.MethodPatch,
		"/api/v1/orgs/"+f.project.OrgID+"/machines/"+testPublicID(t, publicid.KindMachine, f.process.MachineUUID),
		`{"programmatic_tool_calls":`+map[bool]string{true: "true", false: "false"}[enabled]+`}`, "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	require.Equal(t, enabled, machine["programmatic_tool_calls"])
}

func TestDaemonProgramToolCallRoutes(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	ctx := t.Context()
	store := f.project.Store
	execution := store.Execution()
	offer := func() bool {
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
	require.False(t, offer(), "a machine without the flag must not offer tool calls")
	f.setMachineFlag(t, true)
	require.True(t, offer())
	for _, process := range []daemonProcessFixture{f.process, f.base} {
		_, found, err := acceptDaemonProcessOfferForTest(ctx, store, process.authority(), process.ProcessUUID)
		require.NoError(t, err)
		require.True(t, found)
	}

	// Authentication and authorization.
	body := `{"key":"` + uuid.NewString() + `","name":"storage_list","input":{}}`
	f.do(t, http.MethodPost, "/tool-calls", "", body, http.StatusUnauthorized)
	f.do(t, http.MethodPost, "/tool-calls", f.project.AdminToken, body, http.StatusForbidden)
	f.setMachineFlag(t, false)
	f.submit(t, "k", toolcatalog.ToolNameStorageList, `{}`, http.StatusForbidden)
	f.setMachineFlag(t, true)
	other := f.process
	other.ProcessID = testPublicID(t, publicid.KindProcess, f.base.ProcessUUID)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/daemon/processes/"+other.ProcessID+"/tool-calls",
		bytes.NewReader([]byte(body))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.process.Token)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusNotFound, w.Code, "a process whose agent did not opt in: %s", w.Body.String())

	// Validation (the schema rejects malformed requests first) and eligibility.
	f.submit(t, "", toolcatalog.ToolNameStorageList, `{}`, http.StatusBadRequest)
	f.do(t, http.MethodPost, "/tool-calls", f.process.Token,
		`{"key":"not-a-uuid","name":"storage_list","input":{}}`, http.StatusBadRequest)
	f.submit(t, "k", toolcatalog.ToolNameStorageList, `[1]`, http.StatusBadRequest)
	f.submit(t, "k", "bad\u0000name", `{}`, http.StatusNotFound)
	f.submit(t, "k", "no_such_tool", `{}`, http.StatusNotFound)
	f.submit(t, "k", toolcatalog.ToolNameRunCommand, `{"command":"ls"}`, http.StatusForbidden)
	f.submit(t, "k", toolcatalog.ToolNameWebSearch, `{"query":"x"}`, http.StatusForbidden)
	f.submit(t, "k", toolcatalog.ToolNameToolSearch, `{"pattern":"("}`, http.StatusUnprocessableEntity)

	// tool_search is answered inline from callable tools only.
	search := f.submit(t, "search", toolcatalog.ToolNameToolSearch, `{"pattern":".*"}`, http.StatusOK)
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
	require.NoError(t, json.Unmarshal(structuredResult(t, search), &listing))
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
	t.Logf("callable tools: %v", names)

	// A call stays pending without a worker; replays are idempotent on key.
	pending := f.submit(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, http.StatusOK)
	require.Equal(t, daemonprotocol.ProgramToolCallStatePending, pending.State)
	require.NotEmpty(t, pending.ToolCallID)
	replay := f.submit(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, http.StatusOK)
	require.Equal(t, pending.ToolCallID, replay.ToolCallID)
	f.submit(t, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/memory"}`, http.StatusConflict)
	polled := f.do(t, http.MethodGet, "/tool-calls/"+pending.ToolCallID+"?wait_ms=0", f.process.Token, "",
		http.StatusOK)
	require.Equal(t, daemonprotocol.ProgramToolCallStatePending, polled.State)
	f.do(t, http.MethodGet, "/tool-calls/"+testPublicID(t, publicid.KindToolCall, uuid.New())+"?wait_ms=0",
		f.process.Token, "", http.StatusNotFound)
	f.do(t, http.MethodGet, "/tool-calls/"+testPublicID(t, publicid.KindToolCall, f.process.ToolCallUUID)+
		"?wait_ms=0", f.process.Token, "", http.StatusNotFound)

	// A long poll returns as soon as a worker completes the call.
	type pollResult struct {
		recorder *httptest.ResponseRecorder
		elapsed  time.Duration
	}
	polls := make(chan pollResult, 1)
	started := time.Now()
	poll := httptest.NewRequest(http.MethodGet, "/api/v1/daemon/processes/"+f.process.ProcessID+"/tool-calls/"+
		pending.ToolCallID+"?wait_ms=20000", nil).WithContext(ctx)
	poll.Header.Set("Authorization", "Bearer "+f.process.Token)
	go func() {
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, poll)
		polls <- pollResult{recorder: recorder, elapsed: time.Since(started)}
	}()
	// An allowed call runs under its authorization claim.
	executor := tools.Executor{Store: store}
	claim, found, err := execution.ClaimNextProgramToolCall(ctx, uuid.New(), time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ProgramToolCallPhaseAuthorize, claim.Phase)
	scope := tools.NewAsyncExecutionScope(nil)
	require.NoError(t, executor.RunProgramToolCall(tools.WithAsyncExecutionScope(ctx, scope), claim, time.Minute))
	scope.Seal()
	<-scope.Done()
	require.NoError(t, scope.Err())
	var completed pollResult
	select {
	case completed = <-polls:
	case <-time.After(15 * time.Second):
		t.Fatal("long poll did not return after completion")
	}
	require.Less(t, completed.elapsed, 15*time.Second)
	require.Equal(t, http.StatusOK, completed.recorder.Code, completed.recorder.Body.String())
	var result daemonprotocol.ProgramToolCallResult
	require.NoError(t, json.Unmarshal(completed.recorder.Body.Bytes(), &result))
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, result.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeSucceeded, result.Outcome)
	require.Contains(t, string(structuredResult(t, result)), "entries")

	// The public tool-call API shows the child with its parent.
	listed := requestJSONWithHeaders(t, f.handler, http.MethodGet,
		"/api/v1/orgs/"+f.project.OrgID+"/projects/"+f.project.ProjectID+"/agents/"+testPublicID(t, publicid.KindAgent, f.process.AgentUUID)+
			"/tool-calls", "", "", http.StatusOK, authHeaders(f.project.AdminToken))
	var child map[string]any
	items, ok := listed["data"].([]any)
	require.True(t, ok)
	for _, item := range items {
		call, ok := item.(map[string]any)
		require.True(t, ok)
		if call["id"] == pending.ToolCallID {
			child = call
		}
	}
	require.NotNil(t, child, "child call missing from %v", listed)
	require.Equal(t, "program", child["caller"])
	require.Equal(t, testPublicID(t, publicid.KindToolCall, f.process.ToolCallUUID), child["parent_tool_call_id"])
	require.Equal(t, "completed", child["state"])

	// parent_tool_call_id narrows the list to that run_command call's children.
	parentID := testPublicID(t, publicid.KindToolCall, f.process.ToolCallUUID)
	children := requestJSONWithHeaders(t, f.handler, http.MethodGet,
		"/api/v1/orgs/"+f.project.OrgID+"/projects/"+f.project.ProjectID+"/agents/"+testPublicID(t, publicid.KindAgent, f.process.AgentUUID)+
			"/tool-calls?parent_tool_call_id="+parentID, "", "", http.StatusOK, authHeaders(f.project.AdminToken))
	childItems, ok := children["data"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, childItems)
	for _, item := range childItems {
		call, ok := item.(map[string]any)
		require.True(t, ok)
		require.Equal(t, parentID, call["parent_tool_call_id"])
	}

	// A config change applies to the running process: a tool it excludes is
	// refused on submit, and a call submitted before the change fails when a
	// worker picks it up.
	stale := f.submit(t, "stale", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, http.StatusOK)
	require.Equal(t, daemonprotocol.ProgramToolCallStatePending, stale.State)
	f.changeConfig(t, programToolAgentYAML("web_search, storage_list"))
	f.submit(t, "revoked", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, http.StatusForbidden)
	claim, found, err = execution.ClaimNextProgramToolCall(ctx, uuid.New(), time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, executor.RunProgramToolCall(ctx, claim, time.Minute))
	revoked := f.do(t, http.MethodGet, "/tool-calls/"+stale.ToolCallID+"?wait_ms=0", f.process.Token, "",
		http.StatusOK)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, revoked.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeFailed, revoked.Outcome)
	// Re-allowing the tool applies to the same process too.
	f.changeConfig(t, programToolAgentYAML("web_search"))
	f.submit(t, "restored", toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`, http.StatusOK)
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
