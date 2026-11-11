//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type programToolFixture struct {
	integrationToolFixture
	ProcessID uuid.UUID
	ParentID  uuid.UUID
	Authority executionstore.DaemonRuntimeAuthority
}

func newProgramToolFixture(t *testing.T, ctx context.Context, label string, withMCP bool) programToolFixture {
	t.Helper()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, label, toolFixtureOptions{
		withMCP: withMCP, mcpDeferred: true, withMemory: true, withMachine: true,
		extraTools: "  storage_list: {deferred: true}\n" +
			"  storage_read:\n    deferred: true\n    permission: {mode: always_ask}\n" +
			"  storage_search:\n    deferred: true\n    permission: {mode: always_deny}\n" +
			"  storage_write: {deferred: true}\n  web_search: {}\n",
		extraYAML: "deferred_tools:\n  mode: code_mode\n",
	})
	execution := fixture.Store.Execution()
	enabled := true
	_, err := execution.UpdateMachine(ctx, executionstore.UpdateMachineInput{
		OrgID: toolsTestOrgID, MachineID: fixture.Machine.MachineID, ProgrammaticToolCalls: &enabled})
	require.NoError(t, err)
	if withMCP {
		markIntegrationMCPConnectionReady(t, ctx, fixture)
	}
	call := fixture.recordToolCall(t, ctx, "call_program_parent", toolcatalog.ToolNameRunCommand,
		`{"command":"sleep 60"}`, fixture.Now.Add(time.Second))
	parentID := fixture.toolCallID(t, ctx, call.ID)
	var bindingID uuid.UUID
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT id FROM agent_machine_bindings WHERE agent_id = $1`, fixture.Agent.ID).Scan(&bindingID))
	process, err := storagetest.StartProcessForToolCall(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, ToolCallID: parentID, RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		ExecutionSpec: processcmd.ForShell("sleep 60", "sh", "pipe"), AgentMachineBindingID: bindingID, Cwd: "/work",
	})
	require.NoError(t, err)
	authority := executionstore.DaemonRuntimeAuthority{OrgID: toolsTestOrgID, MachineID: fixture.Machine.MachineID,
		DaemonRuntimeID: fixture.Machine.RuntimeID, DaemonTokenID: fixture.Machine.DaemonTokenID}
	_, found, err := execution.AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: authority, ProcessID: process.ID})
	require.NoError(t, err)
	require.True(t, found)
	_, err = execution.MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, ID: process.ID, Authority: authority,
		SourceStartedAt: fixture.Now.Add(2 * time.Second)})
	require.NoError(t, err)
	scope, found, err := execution.GetDaemonProgramToolScope(ctx, toolsTestOrgID, fixture.Machine.MachineID, process.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, process.ID, scope.ProcessID)
	return programToolFixture{integrationToolFixture: fixture, ProcessID: process.ID, ParentID: parentID,
		Authority: authority}
}

func (f programToolFixture) create(
	t *testing.T, ctx context.Context, key, name, input string,
) executionstore.ProgramToolCallRecord {
	t.Helper()
	toolType := toolcatalog.ToolTypeBuiltIn
	if toolcatalog.IsMCPRuntimeToolName(name) {
		toolType = toolcatalog.ToolTypeMCP
	}
	record, err := f.Store.Execution().CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ProcessID: f.ProcessID, Key: programKey(key), Name: name,
		Type: toolType, Input: json.RawMessage(input)})
	require.NoError(t, err)
	return record
}

func (f programToolFixture) claim(
	t *testing.T, ctx context.Context, id uuid.UUID, phase executionstore.ProgramToolCallPhase,
) executionstore.ProgramToolCallClaim {
	t.Helper()
	claim, found, err := f.Store.Execution().ClaimProgramToolCall(ctx, executionstore.ProgramToolCallRef{
		ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ProcessID: f.ProcessID, ToolCallID: id,
		ClaimID: uuid.New()}, time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, phase, claim.Phase)
	return claim
}

func (f programToolFixture) run(
	t *testing.T, ctx context.Context, executor Executor, claim executionstore.ProgramToolCallClaim,
) {
	t.Helper()
	scope := NewAsyncExecutionScope(nil)
	require.NoError(t, executor.RunProgramToolCall(WithAsyncExecutionScope(ctx, scope), claim, time.Minute))
	settleIntegrationAsyncDispatch(t, scope)
	require.NoError(t, scope.Err())
}

func (f programToolFixture) get(t *testing.T, ctx context.Context, id uuid.UUID) executionstore.ProgramToolCallRecord {
	t.Helper()
	record, found, err := f.Store.Execution().GetProgramToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.ProcessID, id)
	require.NoError(t, err)
	require.True(t, found)
	return record
}

func TestProgramToolCallsRunUnderPermissionModes(t *testing.T) {
	ctx := t.Context()
	fixture := newProgramToolFixture(t, ctx, "program-permissions", false)
	executor := Executor{Store: fixture.Store}
	events := func() int {
		var count int
		require.NoError(t, fixture.Pool.QueryRow(ctx,
			`SELECT count(*) FROM agent_events WHERE agent_id = $1`, fixture.Agent.ID).Scan(&count))
		return count
	}
	eventsBefore := events()

	specs, _, err := AgentConfigToolSpecs(
		ctx, fixture.Store, toolsTestProjectID, fixture.Agent.ID, fixture.AgentConfig.ID)
	require.NoError(t, err)
	specs = modelcontext.ProgramCallableTools(specs)
	var names []string
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	require.ElementsMatch(t, []string{toolcatalog.ToolNameStorageList, toolcatalog.ToolNameStorageRead,
		toolcatalog.ToolNameStorageSearch}, names, "tools that are not deferred or not allowlisted must not be callable")
	search, err := ProgramToolSearch(specs, json.RawMessage(`{"pattern":".*"}`))
	require.NoError(t, err)
	require.Equal(t, 3, search.Total)
	require.Len(t, search.Tools, 3)
	outputSchemas := map[string]json.RawMessage{}
	for _, tool := range search.Tools {
		require.NotEmpty(t, tool.OutputSchema, tool.Name)
		outputSchemas[tool.Name] = tool.OutputSchema
	}
	search, err = ProgramToolSearch(specs, json.RawMessage(`{"pattern":"storage_l","max_results":1}`))
	require.NoError(t, err)
	require.Equal(t, toolcatalog.ToolNameStorageList, search.Tools[0].Name)
	_, err = ProgramToolSearch(specs, json.RawMessage(`{"pattern":"("}`))
	require.Error(t, err)

	list := fixture.create(t, ctx, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/memory"}`)
	fixture.run(t, ctx, executor, fixture.claim(t, ctx, list.ID, executionstore.ProgramToolCallPhaseAuthorize))
	listed := fixture.get(t, ctx, list.ID)
	require.Equal(t, executionstore.ToolCallStateCompleted, listed.State)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, listed.Outcome)
	require.Contains(t, string(listed.ResultContentParts), `"/memory"`)
	requireMatchesOutputSchema(t, outputSchemas[toolcatalog.ToolNameStorageList],
		firstStructuredValue(t, listed.ResultContentParts))

	scope := memorystore.Scope{OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID,
		Principal: toolsTestUserPrincipal(fixture.User.ID)}
	memory, err := fixture.Store.Memories().Resolve(ctx, toolsTestProjectID, "engineering")
	require.NoError(t, err)
	_, err = fixture.Store.Memories().Write(ctx, memorystore.WriteInput{Scope: scope, StoreID: memory.ID,
		Path: "notes.md", Content: []byte("program note")})
	require.NoError(t, err)
	read := fixture.create(t, ctx, "read", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	fixture.run(t, ctx, executor, fixture.claim(t, ctx, read.ID, executionstore.ProgramToolCallPhaseAuthorize))
	require.Equal(t, executionstore.ToolCallStateAwaitingPermission, fixture.get(t, ctx, read.ID).State)
	interaction := integrationToolInteraction(t, ctx, fixture.integrationToolFixture, read.ID,
		executionstore.AgentInteractionKindPermission)
	require.Contains(t, string(interaction.Request), toolcatalog.ToolNameStorageRead)
	approveToolPermissionForTest(t, ctx, fixture.Store.Execution(), interaction, fixture.User.ID)
	require.Equal(t, executionstore.ToolCallStateReady, fixture.get(t, ctx, read.ID).State)
	fixture.run(t, ctx, executor, fixture.claim(t, ctx, read.ID, executionstore.ProgramToolCallPhaseExecute))
	readResult := fixture.get(t, ctx, read.ID)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, readResult.Outcome)
	require.Contains(t, string(readResult.ResultContentParts), "program note")
	require.NotContains(t, string(readResult.ResultContentParts), `"digest"`, "hidden parts must be dropped")
	requireMatchesOutputSchema(t, outputSchemas[toolcatalog.ToolNameStorageRead],
		firstStructuredValue(t, readResult.ResultContentParts))

	stale := fixture.create(t, ctx, "stale-approval", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	fixture.run(t, ctx, executor, fixture.claim(t, ctx, stale.ID, executionstore.ProgramToolCallPhaseAuthorize))
	approveToolPermissionForTest(t, ctx, fixture.Store.Execution(), integrationToolInteraction(t, ctx,
		fixture.integrationToolFixture, stale.ID, executionstore.AgentInteractionKindPermission), fixture.User.ID)
	const askRead = "storage_read:\n    deferred: true\n    permission: {mode: always_ask}"
	require.Contains(t, fixture.AgentConfig.Source, askRead)
	denyingYAML := strings.Replace(fixture.AgentConfig.Source, askRead,
		"storage_read:\n    deferred: true\n    permission: {mode: always_deny}", 1)
	compiled := compileToolsAgentYAMLResolved(t, ctx, fixture.Store, fixture.User.ID, denyingYAML)
	denying, err := fixture.Store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID: toolsTestProjectID, Source: denyingYAML, SourceFormat: "yaml",
		ConfiguredModelID:  parseConfiguredModelID(t, compiled),
		CompiledDefinition: json.RawMessage(compiled.CanonicalJSON), EffectiveDefinitionHash: compiled.Hash})
	require.NoError(t, err)
	useConfig := func(id uuid.UUID) {
		t.Helper()
		_, err := fixture.Pool.Exec(ctx, `UPDATE agents SET current_config_id = $1 WHERE id = $2`, id, fixture.Agent.ID)
		require.NoError(t, err)
	}
	useConfig(denying.ID)
	fixture.run(t, ctx, executor, fixture.claim(t, ctx, stale.ID, executionstore.ProgramToolCallPhaseExecute))
	staleResult := fixture.get(t, ctx, stale.ID)
	require.Equal(t, executionstore.ToolResultOutcomeDenied, staleResult.Outcome, string(staleResult.ResultContentParts))
	require.NotContains(t, string(staleResult.ResultContentParts), "program note")
	useConfig(fixture.AgentConfig.ID)

	searchCall := fixture.create(t, ctx, "search", toolcatalog.ToolNameStorageSearch,
		`{"path":"/memory/**","args":["-e","note"]}`)
	fixture.run(t, ctx, executor,
		fixture.claim(t, ctx, searchCall.ID, executionstore.ProgramToolCallPhaseAuthorize))
	denied := fixture.get(t, ctx, searchCall.ID)
	require.Equal(t, executionstore.ToolResultOutcomeDenied, denied.Outcome, string(denied.ResultContentParts))

	for _, name := range []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameStorageWrite} {
		forced := fixture.create(t, ctx, "forced-"+name, name, `{"query":"x"}`)
		fixture.run(t, ctx, executor, fixture.claim(t, ctx, forced.ID, executionstore.ProgramToolCallPhaseAuthorize))
		rejected := fixture.get(t, ctx, forced.ID)
		require.Equal(t, executionstore.ToolResultOutcomeFailed, rejected.Outcome, name)
		require.Contains(t, string(rejected.ResultContentParts), "not enabled", name)
	}

	broken := fixture.create(t, ctx, "internal-error", toolcatalog.ToolNameStorageList, `{}`)
	brokenClaim := fixture.claim(t, ctx, broken.ID, executionstore.ProgramToolCallPhaseAuthorize)
	brokenClaim.ModelCallContextID = uuid.New()
	require.ErrorContains(t, executor.RunProgramToolCall(ctx, brokenClaim, time.Minute), "model call context")
	failed := fixture.get(t, ctx, broken.ID)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, failed.Outcome)
	require.Contains(t, string(failed.ResultContentParts), programToolCallInternalError)
	require.NotContains(t, string(failed.ResultContentParts), "model call context")

	require.Equal(t, eventsBefore, events(), "program tool calls must not write agent events")
	parent, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, fixture.ParentID)
	require.NoError(t, err)
	require.Equal(t, "model", parent.Caller)
	require.Equal(t, fixture.ParentID, listed.ParentToolCallID)
	require.Equal(t, "program", listed.Caller)
}

func TestProgramMCPToolCallRunsAsyncUnderRenewedClaim(t *testing.T) {
	ctx := t.Context()
	fixture := newProgramToolFixture(t, ctx, "program-mcp", true)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	client := &ownedMCPToolClient{callTool: func(ctx context.Context) (*sdkmcp.CallToolResult, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello"}}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	executor := Executor{Store: fixture.Store, MCP: client}
	name := toolcatalog.MCPRuntimeToolName("docs", "greet")
	call := fixture.create(t, ctx, "greet", name, `{"name":"Ada"}`)
	claim := fixture.claim(t, ctx, call.ID, executionstore.ProgramToolCallPhaseAuthorize)
	scope := NewAsyncExecutionScope(NewAsyncExecutionLimiter(1))
	require.NoError(t, executor.RunProgramToolCall(WithAsyncExecutionScope(ctx, scope), claim, time.Minute))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP call did not start")
	}
	require.True(t, scope.Started())
	require.Equal(t, executionstore.ToolCallStateWaiting, fixture.get(t, ctx, call.ID).State)
	ref := executionstore.ProgramToolCallRef{ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID,
		ProcessID: fixture.ProcessID, ToolCallID: call.ID, ClaimID: claim.ClaimID}
	err := fixture.Store.Execution().RenewProgramToolCallClaim(ctx, ref, time.Minute)
	require.NoError(t, err)
	close(release)
	settleIntegrationAsyncDispatch(t, scope)
	require.NoError(t, scope.Err())
	done := fixture.get(t, ctx, call.ID)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, done.Outcome)
	require.Contains(t, string(done.ResultContentParts), "hello")

	blocked := fixture.create(t, ctx, "blocked", name, `{"name":"Grace"}`)
	claim = fixture.claim(t, ctx, blocked.ID, executionstore.ProgramToolCallPhaseAuthorize)
	release = make(chan struct{})
	scope = NewAsyncExecutionScope(nil)
	require.NoError(t, executor.RunProgramToolCall(WithAsyncExecutionScope(ctx, scope), claim, time.Minute))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("second MCP call did not start")
	}
	exitCode := 0
	_, err = fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
		Authority: fixture.Authority, ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, ID: fixture.ProcessID,
		State: executionstore.ProcessStateExited, ExitCode: &exitCode, Result: json.RawMessage(`{"output":""}`),
		SourceEndedAt: fixture.Now.Add(time.Minute)})
	require.NoError(t, err)
	ref.ToolCallID, ref.ClaimID = blocked.ID, claim.ClaimID
	err = fixture.Store.Execution().RenewProgramToolCallClaim(ctx, ref, time.Minute)
	require.ErrorIs(t, err, storeerr.ErrProgramToolCallClaimLost)
	reaped, err := fixture.Store.Execution().ReapProgramToolCalls(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, reaped)
	close(release)
	settleIntegrationAsyncDispatch(t, scope)
	require.ErrorIs(t, scope.Err(), storeerr.ErrRuntimeLockInactive, "a late result must not overwrite the reap")
	var outcome string
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT outcome FROM tool_call_results WHERE tool_call_id = $1`, blocked.ID).Scan(&outcome))
	require.Equal(t, string(executionstore.ToolResultOutcomeCanceled), outcome)
}

func TestProgramToolCallInterruptedRunIsReleased(t *testing.T) {
	ctx := t.Context()
	fixture := newProgramToolFixture(t, ctx, "interrupted", false)
	executor := Executor{Store: fixture.Store}
	stopped := make(chan struct{})
	close(stopped)
	failed, err := structuredParts(map[string]any{"error_code": "async_tool_interrupted"})
	require.NoError(t, err)
	failure := executionstore.ToolResultOutcomeFailed
	waiting := func(key string) (executionstore.ProgramToolCallRecord, programClaimAuthority, Turn) {
		call := fixture.create(t, ctx, key, toolcatalog.ToolNameStorageList, `{"pattern":"/artifacts"}`)
		claim := fixture.claim(t, ctx, call.ID, executionstore.ProgramToolCallPhaseAuthorize)
		_, err := fixture.Store.Execution().AuthorizeProgramToolCall(ctx, claim.Ref(), time.Minute)
		require.NoError(t, err)
		authority := programClaimAuthority{ref: claim.Ref(), lease: time.Minute, stopped: stopped}
		return call, authority, Turn{ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, authority: authority}
	}

	call, authority, turn := waiting("released")
	released, err := authority.completeRunning(ctx, executor, turn, call.ID, failure, failed)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, released.State)
	fixture.run(t, ctx, executor, fixture.claim(t, ctx, call.ID, executionstore.ProgramToolCallPhaseExecute))
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, fixture.get(t, ctx, call.ID).Outcome)

	call, authority, turn = waiting("finished")
	done, err := authority.completeRunning(ctx, executor, turn, call.ID, executionstore.ToolResultOutcomeSucceeded, failed)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, done.State)

	call, authority, turn = waiting("started")
	authority.started = new(atomic.Bool)
	turn.authority = authority
	turn.markStarted()
	done, err = authority.completeRunning(ctx, executor, turn, call.ID, failure, failed)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, done.State)
	require.Equal(t, executionstore.ToolResultOutcomeFailed, done.Outcome)
}

func TestToolSearchReturnsCodeModeToolsOnlyWithAMachineThatAllowsThem(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "code-mode-search", toolFixtureOptions{
		withMemory: true, withMachine: true,
		extraTools: "  storage_list: {deferred: true}\n  storage_read: {deferred: true}\n" +
			"  storage_search: {deferred: true}\n  storage_write: {deferred: true}\n",
		extraYAML: "deferred_tools:\n  mode: code_mode\n",
	})
	setMachineFlag := func(enabled bool) {
		t.Helper()
		_, err := fixture.Store.Execution().UpdateMachine(ctx, executionstore.UpdateMachineInput{
			OrgID: toolsTestOrgID, MachineID: fixture.Machine.MachineID, ProgrammaticToolCalls: &enabled})
		require.NoError(t, err)
	}
	setMachineFlag(true)
	specs, _, err := AgentConfigToolSpecs(
		ctx, fixture.Store, toolsTestProjectID, fixture.Agent.ID, fixture.AgentConfig.ID)
	require.NoError(t, err)
	turn := Turn{ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, Tools: map[string]ToolSpec{}}
	for _, spec := range specs {
		turn.Tools[spec.Name] = ToolSpecFromModelContext(spec)
	}
	input := json.RawMessage(`{"pattern":"storage"}`)
	fixture.recordToolCalls(t, ctx, []model.ToolCall{
		{ID: "call_search_code_mode", Name: toolcatalog.ToolNameToolSearch, Input: input},
		{ID: "call_search_tool_add", Name: toolcatalog.ToolNameToolSearch, Input: input},
	}, fixture.Now.Add(time.Second))
	search := func(providerCallID string) modelcontext.ToolSearchResult {
		t.Helper()
		var parts json.RawMessage
		_, err := fixture.Store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
			ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, ToolCallID: fixture.toolCallID(t, ctx, providerCallID),
			RuntimeLockID: fixture.Lock.ID,
		}, func(reader *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			result, err := runToolSearch(ctx, transactionalToolContext{Reader: reader, Turn: turn, Call: model.ToolCall{
				Name: toolcatalog.ToolNameToolSearch, Input: input}})
			if err != nil {
				return nil, err
			}
			completed, ok := result.(completeTransaction)
			if !ok {
				return nil, fmt.Errorf("tool_search returned %T", result)
			}
			if parts, err = completed.content.contentParts(); err != nil {
				return nil, err
			}
			return executionstore.CompleteToolCallInExecution(executionstore.ToolCallCompletionInput{
				Outcome: executionstore.ToolResultOutcomeSucceeded, ResultContentParts: parts}), nil
		})
		require.NoError(t, err)
		found, ok := modelcontext.ToolSearchResultFromToolResult(modelcontext.ToolResultRef{
			Name: toolcatalog.ToolNameToolSearch, ContentParts: parts})
		require.True(t, ok)
		return found
	}

	withMachine := search("call_search_code_mode")
	require.Equal(t, []string{toolcatalog.ToolNameStorageWrite}, withMachine.ToolNames,
		"a deferred tool commands cannot call is added")
	var names []string
	for _, tool := range withMachine.CodeModeTools {
		require.NotEmpty(t, tool.OutputSchema, tool.Name)
		names = append(names, tool.Name)
	}
	require.Equal(t, []string{toolcatalog.ToolNameStorageList, toolcatalog.ToolNameStorageRead,
		toolcatalog.ToolNameStorageSearch}, names)

	setMachineFlag(false)
	withoutMachine := search("call_search_tool_add")
	require.Empty(t, withoutMachine.CodeModeTools)
	require.Equal(t, []string{toolcatalog.ToolNameStorageList, toolcatalog.ToolNameStorageRead,
		toolcatalog.ToolNameStorageSearch, toolcatalog.ToolNameStorageWrite}, withoutMachine.ToolNames)
}

func programKey(label string) uuid.UUID { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)) }
