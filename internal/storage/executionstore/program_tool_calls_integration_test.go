//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestProgramToolCallsStore(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "program-tool-calls")
	store, execution := fixture.Store, fixture.Store.Execution()
	config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), execution, testOrgID, testProjectID,
		testAgentConfigYAML()+"programmatic_tool_calls:\n  enabled: true\n  exclude: [storage_write]\n")
	_, err := store.pool.Exec(ctx, `UPDATE agents SET current_config_id=$1 WHERE id=$2`, config.ID, fixture.AgentID)
	require.NoError(t, err)
	parent := createToolCallForProcessTest(t, ctx, fixture, "program_parent", "run_command")
	process, err := startProcessForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: parent, RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{ExecutionSpec: processcmd.ForShell("sleep 60", "sh", ""),
		AgentMachineBindingID: fixture.BindingID, Cwd: "/work"})
	require.NoError(t, err)
	setMachineFlag := func(enabled bool) {
		t.Helper()
		machine, err := execution.UpdateMachine(ctx, executionstore.UpdateMachineInput{
			OrgID: testOrgID, MachineID: fixture.MachineID, ProgrammaticToolCalls: &enabled})
		require.NoError(t, err)
		require.Equal(t, enabled, machine.ProgrammaticToolCalls)
	}
	offerFlag := func() bool {
		t.Helper()
		offers, err := execution.ListDaemonProcessOffers(ctx, executionstore.DaemonWorkInput{
			Authority: fixture.authority(), Limit: 32})
		require.NoError(t, err)
		require.Len(t, offers, 1)
		return offers[0].ProgrammaticToolCalls
	}
	require.False(t, offerFlag())
	setMachineFlag(true)
	require.True(t, offerFlag())
	_, found, err := acceptDaemonProcessForTest(ctx, store, testOrgID, fixture.MachineID, fixture.RuntimeID, process.ID)
	require.NoError(t, err)
	require.True(t, found)
	_, err = execution.MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ID: process.ID, Authority: fixture.authority(),
		SourceStartedAt: fixture.Now.Add(time.Second)})
	require.NoError(t, err)

	scope, found, err := execution.GetDaemonProgramToolScope(ctx, testOrgID, fixture.MachineID, process.ID)
	require.NoError(t, err)
	require.True(t, found)
	parentRecord, err := execution.GetToolCall(ctx, testProjectID, fixture.AgentID, parent)
	require.NoError(t, err)
	require.Equal(t, "model", parentRecord.Caller)
	require.Equal(t, executionstore.ProgramToolScope{ProjectID: testProjectID, AgentID: fixture.AgentID,
		ProcessID: process.ID, AgentConfigID: config.ID}, scope)
	setMachineFlag(false)
	_, found, err = execution.GetDaemonProgramToolScope(ctx, testOrgID, fixture.MachineID, process.ID)
	require.NoError(t, err)
	require.False(t, found)
	setMachineFlag(true)

	create := func(key string) executionstore.ProgramToolCallRecord {
		t.Helper()
		record, err := execution.CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, ProcessID: process.ID, Key: programKey(key),
			Name: "storage_read", Type: "built_in", Input: json.RawMessage(`{"path":"notes.md"}`)})
		require.NoError(t, err)
		return record
	}
	claim := func(phase executionstore.ProgramToolCallPhase, id uuid.UUID) executionstore.ProgramToolCallRef {
		t.Helper()
		claimID := uuid.New()
		claimed, found, err := execution.ClaimNextProgramToolCall(ctx, claimID, time.Minute)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, phase, claimed.Phase)
		require.Equal(t, id, claimed.ID)
		require.Equal(t, claimID, claimed.ClaimID)
		return executionstore.ProgramToolCallRef{ProjectID: testProjectID, AgentID: fixture.AgentID,
			ProcessID: process.ID, ToolCallID: id, ClaimID: claimID}
	}
	authorized := func(id uuid.UUID) executionstore.ProgramToolCallRef {
		t.Helper()
		ref := claim(executionstore.ProgramToolCallPhaseAuthorize, id)
		authorized, err := execution.AuthorizeProgramToolCall(ctx, ref, time.Minute)
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStateWaiting, authorized.State)
		waiting, found, err := execution.GetProgramToolCall(ctx, testProjectID, fixture.AgentID, process.ID, id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, executionstore.ToolCallStateWaiting, waiting.State)
		return ref
	}

	first := create("first")
	require.True(t, first.Created)
	require.Equal(t, "program", first.Caller)
	require.Equal(t, parent, first.ParentToolCallID)
	require.Equal(t, parentRecord.TurnID, first.TurnID)
	replay := create("first")
	require.False(t, replay.Created)
	require.Equal(t, first.ID, replay.ID)
	_, err = execution.CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ProcessID: process.ID, Key: programKey("first"),
		Name: "storage_list", Type: "built_in", Input: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)

	// Agent row locks are skipped, not waited on.
	locked, err := store.pool.Begin(ctx)
	require.NoError(t, err)
	_, err = locked.Exec(ctx, `SELECT 1 FROM agents WHERE id=$1 FOR UPDATE`, fixture.AgentID)
	require.NoError(t, err)
	_, found, err = execution.ClaimNextProgramToolCall(ctx, uuid.New(), time.Minute)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, locked.Rollback(ctx))

	ref := authorized(first.ID)
	_, err = execution.ExecuteProgramToolCall(ctx, ref,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartProcessForToolCall(executionstore.CreateProcessInput{}), nil
		})
	require.ErrorIs(t, err, storeerr.ErrInvalidToolCallDisposition)
	started, err := execution.ExecuteProgramToolCall(ctx, ref,
		func(reader *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			call, err := reader.GetToolCall(ctx)
			require.NoError(t, err)
			require.Equal(t, "program", call.Caller)
			return executionstore.StartToolCallAsync(), nil
		})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallDispositionRunning, started.Disposition)
	require.True(t, started.Applied)
	err = execution.RenewProgramToolCallClaim(ctx, ref, time.Minute)
	require.NoError(t, err)
	completed, err := execution.CompleteProgramToolCall(ctx, executionstore.CompleteProgramToolCallInput{
		ProgramToolCallRef: ref, Outcome: executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`)})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, completed.State)
	got, found, err := execution.GetProgramToolCall(ctx, testProjectID, fixture.AgentID, process.ID, first.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, got.Outcome)
	require.JSONEq(t, `[{"type":"structured_data","value":{"ok":true}}]`, string(got.ResultContentParts))
	_, found, err = execution.GetProgramToolCall(ctx, testProjectID, fixture.AgentID, uuid.New(), first.ID)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = execution.GetProgramToolCall(ctx, testProjectID, fixture.AgentID, process.ID, parent)
	require.NoError(t, err)
	require.False(t, found, "the run_command call itself is not a program call")

	second := create("second")
	ref = authorized(second.ID)
	executed, err := execution.ExecuteProgramToolCall(ctx, ref,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CompleteToolCallInExecution(executionstore.ToolCallCompletionInput{
				Outcome:            executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`)}), nil
		})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallDispositionCompleted, executed.Disposition)
	require.Equal(t, "program", executed.Completed.Caller)

	third := create("third")
	ref = claim(executionstore.ProgramToolCallPhaseAuthorize, third.ID)
	request, err := toolpermission.ParseRequest(permissionRequestForStorageTest(t, "storage_read"))
	require.NoError(t, err)
	interaction, err := execution.OpenProgramPermissionInteraction(ctx, ref, request)
	require.NoError(t, err)
	require.Equal(t, third.ID, interaction.ToolCallID)
	var inputs int
	countInputs := func() int {
		t.Helper()
		require.NoError(t, store.pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE agent_id=$1`,
			fixture.AgentID).Scan(&inputs))
		return inputs
	}
	before := countInputs()
	resolve := executionstore.ResolveAgentInteractionInput{ProjectID: testProjectID, AgentID: fixture.AgentID,
		ID: interaction.ID, Actor: mustOmnaraActorParams(t, fixture.UserID),
		Resolution: interactionform.Resolution{Answers: []interactionform.Answer{
			{OptionIndices: []int{toolpermission.AllowOptionIndex}}}}}
	for range 2 {
		resolved, err := execution.ResolveAgentInteraction(ctx, resolve)
		require.NoError(t, err)
		require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
		require.Equal(t, uuid.Nil, resolved.ResolvedByInputID)
	}
	require.Equal(t, before, countInputs())
	ready, err := execution.GetToolCall(ctx, testProjectID, fixture.AgentID, third.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, ready.State)

	listed, err := execution.ListCompletedToolCallsAtWatermark(ctx, testProjectID, fixture.AgentID, 0, 1_000_000_000_000)
	require.NoError(t, err)
	for _, call := range listed {
		require.Equal(t, "model", call.Caller)
	}
	all, err := execution.ListToolCalls(ctx, executionstore.ListToolCallsInput{ProjectID: testProjectID,
		AgentIDs: []uuid.UUID{fixture.AgentID}, Limit: 50})
	require.NoError(t, err)
	callers := map[uuid.UUID]string{}
	for _, call := range all.ToolCalls {
		callers[call.ID] = call.Caller
	}
	require.Equal(t, "program", callers[third.ID])
	require.Equal(t, "model", callers[parent])
	programs, err := execution.ListToolCalls(ctx, executionstore.ListToolCallsInput{ProjectID: testProjectID,
		AgentIDs: []uuid.UUID{fixture.AgentID}, ParentToolCallID: parent, Limit: 50})
	require.NoError(t, err)
	require.NotEmpty(t, programs.ToolCalls)
	for _, call := range programs.ToolCalls {
		require.Equal(t, "program", call.Caller)
		require.Equal(t, parent, call.ParentToolCallID)
	}

	_, err = store.pool.Exec(ctx, `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, process.ID)
	require.NoError(t, err)
	_, err = execution.CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ProcessID: process.ID, Key: programKey("late"),
		Name: "storage_read", Type: "built_in", Input: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, storeerr.ErrProcessTerminal)
	reaped, err := execution.ReapProgramToolCalls(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, reaped)
	canceled, err := execution.GetToolCall(ctx, testProjectID, fixture.AgentID, third.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeCanceled, canceled.Outcome)
	reaped, err = execution.ReapProgramToolCalls(ctx, 10)
	require.NoError(t, err)
	require.Zero(t, reaped)
}

// programKey is a stable idempotency key for a test label.
func programKey(label string) uuid.UUID { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)) }
