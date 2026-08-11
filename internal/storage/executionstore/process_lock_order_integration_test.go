//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestStartProcessLocksMachineBeforeAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "start_process_lock_order")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"start_process_lock_order",
		"run_command",
	)
	transaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}
	input := executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 1",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}
	assertMachineLockPrecedesAgentLock(
		t,
		ctx,
		fixture,
		"start-process-lock-order",
		func(ctx context.Context, store *Store) error {
			_, err := startProcessForTest(ctx, store, transaction, input)
			return err
		},
	)
}

func TestCreateProcessActionLocksMachineBeforeAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "create_process_action_lock_order")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"create_process_action_lock_order",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("create_process_action_lock_order_process", "run_command"),
			builtInProcessToolCallBatchItem("create_process_action_lock_order_action", "write_process"),
		},
	)
	processToolCallID, actionToolCallID := toolCallIDs[0], toolCallIDs[1]
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
	); err != nil || !found {
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	transaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}
	input := executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello"}`),
	}
	assertMachineLockPrecedesAgentLock(
		t,
		ctx,
		fixture,
		"create-process-action-lock-order",
		func(ctx context.Context, store *Store) error {
			_, err := createProcessActionForTest(ctx, store, transaction, input)
			return err
		},
	)
}

func assertMachineLockPrecedesAgentLock(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	applicationName string,
	mutation func(context.Context, *Store) error,
) {
	t.Helper()
	machineTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin machine lock holder: %v", err)
	}
	defer func() { _ = machineTx.Rollback(ctx) }()
	if _, err := machineTx.Exec(
		ctx,
		`SELECT id FROM machines WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		fixture.OrgID,
		fixture.MachineID,
	); err != nil {
		t.Fatalf("lock machine row: %v", err)
	}

	writerConfig := fixture.Store.pool.Config()
	writerConfig.MaxConns = 1
	writerConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	writerPool, err := pgxpool.NewWithConfig(ctx, writerConfig)
	if err != nil {
		t.Fatalf("open mutation pool: %v", err)
	}
	t.Cleanup(writerPool.Close)
	done := make(chan error, 1)
	go func() {
		done <- mutation(context.Background(), newIntegrationStore(writerPool))
	}()
	integrationdb.WaitForApplicationLockWaiter(t, ctx, fixture.Store.pool, applicationName)

	agentTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock probe: %v", err)
	}
	if _, err := agentTx.Exec(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE NOWAIT`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		_ = agentTx.Rollback(ctx)
		t.Fatalf("mutation locked agent before machine: %v", err)
	}
	if err := agentTx.Rollback(ctx); err != nil {
		t.Fatalf("release agent lock probe: %v", err)
	}
	if err := machineTx.Commit(ctx); err != nil {
		t.Fatalf("release machine row: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("mutation after machine unlock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mutation after machine unlock")
	}
}
