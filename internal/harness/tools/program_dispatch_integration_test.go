//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
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

// newProgramToolFixture starts a live run_command process on a machine with
// programmatic tool calls enabled, for an agent that opts in and excludes
// web_search.
func newProgramToolFixture(t *testing.T, ctx context.Context, label string, withMCP bool) programToolFixture {
	t.Helper()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, label, toolFixtureOptions{
		withMCP: withMCP, withMemory: true, withMachine: true,
		extraTools: "  storage_list: {}\n" +
			"  storage_read:\n    permission: {mode: always_ask}\n" +
			"  storage_search:\n    permission: {mode: always_deny}\n" +
			"  storage_write: {}\n  web_search: {}\n",
		extraYAML: "programmatic_tool_calls:\n  enabled: true\n  exclude: [web_search]\n",
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
	claim, found, err := f.Store.Execution().ClaimNextProgramToolCall(ctx, uuid.New(), time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, id, claim.ID)
	require.Equal(t, phase, claim.Phase)
	return claim
}

// run runs one claimed phase like the worker loop and waits for its async work.
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

	specs, contract, err := AgentConfigToolSpecs(
		ctx, fixture.Store, toolsTestProjectID, fixture.Agent.ID, fixture.AgentConfig.ID)
	require.NoError(t, err)
	specs, err = modelcontext.ProgramCallableTools(contract, specs)
	require.NoError(t, err)
	var names []string
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	require.ElementsMatch(t, []string{toolcatalog.ToolNameStorageList, toolcatalog.ToolNameStorageRead,
		toolcatalog.ToolNameStorageSearch}, names, "excluded and non-allowlisted tools must not be callable")
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

	// always_allow runs to completion under its authorization claim.
	list := fixture.create(t, ctx, "list", toolcatalog.ToolNameStorageList, `{"pattern":"/memory"}`)
	fixture.run(t, ctx, executor, fixture.claim(t, ctx, list.ID, executionstore.ProgramToolCallPhaseAuthorize))
	listed := fixture.get(t, ctx, list.ID)
	require.Equal(t, executionstore.ToolCallStateCompleted, listed.State)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, listed.Outcome)
	require.Contains(t, string(listed.ResultContentParts), `"/memory"`)
	requireMatchesOutputSchema(t, outputSchemas[toolcatalog.ToolNameStorageList],
		firstStructuredValue(t, listed.ResultContentParts))

	// always_ask opens the model path's permission interaction; approving runs it.
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

	// always_deny completes denied without running.
	searchCall := fixture.create(t, ctx, "search", toolcatalog.ToolNameStorageSearch,
		`{"path":"/memory/**","args":["-e","note"]}`)
	fixture.run(t, ctx, executor,
		fixture.claim(t, ctx, searchCall.ID, executionstore.ProgramToolCallPhaseAuthorize))
	denied := fixture.get(t, ctx, searchCall.ID)
	require.Equal(t, executionstore.ToolResultOutcomeDenied, denied.Outcome, string(denied.ResultContentParts))

	// Calls that bypassed the API's eligibility check still cannot run.
	for _, name := range []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameStorageWrite} {
		forced := fixture.create(t, ctx, "forced-"+name, name, `{"query":"x"}`)
		fixture.run(t, ctx, executor, fixture.claim(t, ctx, forced.ID, executionstore.ProgramToolCallPhaseAuthorize))
		rejected := fixture.get(t, ctx, forced.ID)
		require.Equal(t, executionstore.ToolResultOutcomeFailed, rejected.Outcome, name)
		require.Contains(t, string(rejected.ResultContentParts), "not enabled", name)
	}

	// An internal failure fails the call without showing the command its cause.
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

	// When the process exits, the claim is lost and the reaper cancels the call.
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

// programKey is a stable idempotency key for a test label.
func programKey(label string) uuid.UUID { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)) }
