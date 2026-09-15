//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestSpawnSubagentAndDeleteMachineDoNotDeadlock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newProcessDaemonFixture(t, ctx, "spawn_delete_machine")
	store := fixture.Store
	toolIDs := createToolCallBatchForProcessTest(t, ctx, fixture, "spawn_delete_machine", []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("spawn_delete_process", "run_command"),
		builtInProcessToolCallBatchItem("spawn_delete_child", "spawn_subagent"),
	})
	process, err := startProcessForTest(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: toolIDs[0], RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID, Command: "cat", ShellSelector: "sh", Cwd: "/work",
	})
	if err != nil {
		t.Fatalf("start parent process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx, store, testOrgID, fixture.MachineID, fixture.RuntimeID, process.ID,
	); err != nil || !found {
		t.Fatalf("accept parent process: found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(time.Second))
	process, err = store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil || process.State != executionstore.ProcessStateRunning {
		t.Fatalf("parent process must be running: process=%+v err=%v", process, err)
	}
	input := subagentLockTestInput(t, ctx, fixture)

	// Deletion takes the machine first. Pause it at its live daemon runtime so
	// the real spawn transaction must wait for that machine before the parent.
	runtimeTx := launchLockTestTransaction(t, ctx, store.pool,
		`SELECT id FROM daemon_runtimes WHERE id = $1 FOR UPDATE`, fixture.RuntimeID)
	blockingPID := int32(runtimeTx.Conn().PgConn().PID())
	deletion := integrationdb.RunAsync(func() (executionstore.MachineRecord, error) {
		return store.Execution().IntegrationDeleteMachineOnce(ctx, executionstore.DeleteMachineInput{
			OrgID: testOrgID, MachineID: fixture.MachineID,
		})
	})
	integrationdb.WaitForLockWaitBlockedBy(
		t, ctx, store.pool, "-- name: ListActiveDaemonRuntimesForUpdate", blockingPID,
	)
	spawn := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return executeToolCallOnceForLockOrder[executionstore.LaunchAgentResult](ctx, store,
			executionstore.ExecuteToolCallInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: toolIDs[1], RuntimeLockID: fixture.Lock.ID,
			}, executionstore.LaunchSubagentForToolCall(input, acceptedPoolMachineCompletionForTest),
		)
	})
	integrationdb.WaitForNamedLockWaitersBlockedByChain(t, ctx, store.pool, "LockMachineForLifecycle", blockingPID, 1)
	parentProbe := launchLockTestTransaction(t, ctx, store.pool,
		`SELECT id FROM agents WHERE id = $1 FOR UPDATE NOWAIT`, fixture.AgentID)
	if err := parentProbe.Rollback(ctx); err != nil {
		t.Fatalf("release parent probe: %v", err)
	}
	if err := runtimeTx.Commit(ctx); err != nil {
		t.Fatalf("release daemon runtime: %v", err)
	}
	deleted := integrationdb.Await(t, deletion, "machine deletion")
	if deleted.Err != nil || deleted.Value.LifecycleState != "deleted" {
		t.Fatalf("delete machine: value=%+v err=%v", deleted.Value, deleted.Err)
	}
	child := integrationdb.Await(t, spawn, "spawn after deletion")
	if child.Err != nil {
		t.Fatalf("spawn after deletion without retry: %v", child.Err)
	}
	if !child.Value.Created || child.Value.Agent.ParentAgentID != fixture.AgentID ||
		len(child.Value.MachineBindings) != 0 {
		t.Fatalf("child must exclude the deleted inherited machine: %+v", child.Value)
	}
	process, err = store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil || process.State != executionstore.ProcessStateUnknown || process.StateReasonCode != "machine_deleted" {
		t.Fatalf("deletion must terminalize the parent's live process: process=%+v err=%v", process, err)
	}
	tool, err := store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolIDs[1])
	if err != nil || tool.State != executionstore.ToolCallStateCompleted ||
		tool.Outcome != executionstore.ToolResultOutcomeSucceeded {
		t.Fatalf("spawn tool must complete atomically: tool=%+v err=%v", tool, err)
	}
	bindings, err := store.Execution().ListAgentMachineBindings(ctx, testProjectID, fixture.AgentID)
	if err != nil || len(bindings) != 1 || bindings[0].State != executionstore.AgentMachineBindingStateReleased {
		t.Fatalf("parent binding must be released: bindings=%+v err=%v", bindings, err)
	}
}

func TestLaunchSubagentRevalidatesMachineBindingsAfterParentLock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newProcessDaemonFixture(t, ctx, "spawn_binding_settings")
	store := fixture.Store
	input := subagentLockTestInput(t, ctx, fixture)
	parentTx := launchLockTestTransaction(t, ctx, store.pool,
		`SELECT id FROM agents WHERE id = $1 FOR UPDATE`, fixture.AgentID)
	spawn := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return store.Execution().IntegrationLaunchAgentOnce(ctx, input)
	})
	integrationdb.WaitForLockWaitBlockedBy(
		t, ctx, store.pool, "-- name: LockAgentInProject", int32(parentTx.Conn().PgConn().PID()),
	)
	// Change the settings while the launch is between machine and parent locks.
	// The child must use the post-lock projection, not the initial candidate rows.
	if _, err := parentTx.Exec(ctx, `UPDATE agent_machine_bindings
		SET cwd = '/current', description = 'current description',
		    env_overlay = '{"CURRENT":"yes"}', secret_env_overlay = '{"TOKEN":"current-token"}'
		WHERE id = $1`, fixture.BindingID); err != nil {
		t.Fatalf("update parent binding: %v", err)
	}
	if err := parentTx.Commit(ctx); err != nil {
		t.Fatalf("release parent: %v", err)
	}
	child := integrationdb.Await(t, spawn, "spawn with current binding settings")
	if child.Err != nil {
		t.Fatalf("spawn with current binding settings: %v", child.Err)
	}
	if len(child.Value.MachineBindings) != 1 {
		t.Fatalf("child bindings = %+v", child.Value.MachineBindings)
	}
	binding := child.Value.MachineBindings[0]
	var env, secretEnv map[string]string
	if err := json.Unmarshal(binding.EnvOverlay, &env); err != nil {
		t.Fatalf("decode child environment: %v", err)
	}
	if err := json.Unmarshal(binding.SecretEnvOverlay, &secretEnv); err != nil {
		t.Fatalf("decode child secret environment: %v", err)
	}
	if binding.MachineID != fixture.MachineID || binding.Cwd != "/current" ||
		binding.Description != "current description" ||
		env["CURRENT"] != "yes" || secretEnv["TOKEN"] != "current-token" {
		t.Fatalf("child did not inherit current binding settings: %+v", binding)
	}
}

func TestLaunchSubagentRetriesWhenEligibleMachineSetGrows(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newProcessDaemonFixture(t, ctx, "spawn_binding_growth")
	store := fixture.Store
	input := subagentLockTestInput(t, ctx, fixture)
	// Bindings are attached even during provisioning in the current schema.
	// Inject a binding becoming eligible outside the parent's sources gate to
	// exercise the defensive retry if an asynchronous activation path is added.
	if _, err := store.pool.Exec(ctx,
		`UPDATE agent_machine_bindings SET state = 'released' WHERE id = $1`, fixture.BindingID,
	); err != nil {
		t.Fatalf("make binding initially ineligible: %v", err)
	}
	parentTx := launchLockTestTransaction(t, ctx, store.pool,
		`SELECT id FROM agents WHERE id = $1 FOR UPDATE`, fixture.AgentID)
	spawn := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return store.Execution().IntegrationLaunchAgentOnce(ctx, input)
	})
	integrationdb.WaitForLockWaitBlockedBy(
		t, ctx, store.pool, "-- name: LockAgentInProject", int32(parentTx.Conn().PgConn().PID()),
	)
	if _, err := parentTx.Exec(ctx,
		`UPDATE agent_machine_bindings SET state = 'attached' WHERE id = $1`, fixture.BindingID,
	); err != nil {
		t.Fatalf("make binding eligible: %v", err)
	}
	// Keep the newly eligible machine busy. Retrying must return without ever
	// trying to lock it while the launch owns the parent.
	machineTx := launchLockTestTransaction(t, ctx, store.pool,
		`SELECT id FROM machines WHERE id = $1 FOR UPDATE`, fixture.MachineID)
	if err := parentTx.Commit(ctx); err != nil {
		t.Fatalf("release parent: %v", err)
	}
	outcome := integrationdb.Await(t, spawn, "retry for new eligible machine")
	if !errors.Is(outcome.Err, storeutil.ErrRetryTransaction) {
		t.Fatalf("launch error = %v, want whole-transaction retry", outcome.Err)
	}
	if _, err := store.q.GetAgentByIdempotencyKey(ctx, dbsqlc.GetAgentByIdempotencyKeyParams{
		ProjectID: testProjectID, IdempotencyKey: input.IdempotencyKey,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retry must leave no child: %v", err)
	}
	parentProbe := launchLockTestTransaction(t, ctx, store.pool,
		`SELECT id FROM agents WHERE id = $1 FOR UPDATE NOWAIT`, fixture.AgentID)
	if err := parentProbe.Rollback(ctx); err != nil {
		t.Fatalf("release parent rollback probe: %v", err)
	}
	if err := machineTx.Commit(ctx); err != nil {
		t.Fatalf("release newly eligible machine: %v", err)
	}
	child, err := store.Execution().LaunchAgent(ctx, input)
	if err != nil || !child.Created || len(child.MachineBindings) != 1 ||
		child.MachineBindings[0].MachineID != fixture.MachineID {
		t.Fatalf("fresh launch must inherit newly eligible machine: child=%+v err=%v", child, err)
	}
	replay, err := store.Execution().LaunchAgent(ctx, input)
	if err != nil {
		t.Fatalf("replay after retry: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replay, child.Agent)
}

func subagentLockTestInput(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
) executionstore.LaunchAgentInput {
	t.Helper()
	parent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	actor, err := executionstore.SubagentActorParams(testOrgID, parent)
	if err != nil {
		t.Fatalf("build parent actor: %v", err)
	}
	return executionstore.LaunchAgentInput{
		ProjectID: testProjectID, AgentConfigID: parent.CurrentConfigID,
		LaunchedBy: systemPrincipalForTest(parent.ID), MessageActor: actor,
		Message: "Help with the current task.", IdempotencyKey: "child:" + parent.ID.String(),
		Subagent: &executionstore.SubagentLaunch{
			ParentAgentID: parent.ID, Key: "helper", MaxDepth: agentconfig.MaxSubagentDepth,
			MaxInstances: new(1), MaxSubagents: new(1),
		},
	}
}

func launchLockTestTransaction(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	args ...any,
) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin launch lock holder: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		t.Fatalf("acquire launch test lock: %v", err)
	}
	return tx
}
