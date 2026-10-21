//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/cleanupcontext"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestPoolMachineCreationAndGrantRevocationSerializePoolBeforeAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "create-revoke")

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: testOrgID, ID: fixture.machinePool.ID},
	); err != nil {
		t.Fatalf("lock machine pool for lifecycle order: %v", err)
	}

	createDone := integrationdb.RunAsync(func() (executionstore.CreatePoolMachineResult, error) {
		result, err := fixture.store.Execution().ExecuteToolCall(ctx, fixture.transaction(fixture.createToolCallID),
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.CreatePoolMachineForToolCall(
					executionstore.CreatePoolMachineInput{MachinePoolID: fixture.machinePool.ID},
					acceptedPoolMachineCompletionForTest,
				), nil
			})
		if err != nil {
			return executionstore.CreatePoolMachineResult{}, err
		}
		created, ok := result.CommandResult.(executionstore.CreatePoolMachineResult)
		if !ok {
			return executionstore.CreatePoolMachineResult{}, fmt.Errorf("unexpected command result %T", result.CommandResult)
		}
		return created, nil
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 1)

	agentLockCtx, cancelAgentLock := context.WithTimeout(ctx, 2*time.Second)
	defer cancelAgentLock()
	if _, err := controlQ.LockAgentInProject(
		agentLockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent while pool-machine creation waits for pool: %v", err)
	}

	revokeDone := integrationdb.RunAsync(func() (executionstore.DeleteProjectMachinePoolGrantResult, error) {
		return fixture.store.Execution().IntegrationDeleteProjectMachinePoolGrantOnce(
			ctx,
			testOrgID,
			testProjectID,
			fixture.poolGrant.ID,
		)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForUpdate", 1)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release lifecycle lock control transaction: %v", err)
	}

	createOutcome := integrationdb.Await(t, createDone, "pool-machine creation")
	if createOutcome.Err != nil {
		t.Fatalf("create pool machine in one transaction attempt: %v", createOutcome.Err)
	}
	created := createOutcome.Value
	revokeOutcome := integrationdb.Await(t, revokeDone, "project machine-pool grant revocation")
	if revokeOutcome.Err != nil {
		t.Fatalf("revoke project machine-pool grant: %v", revokeOutcome.Err)
	}
	revoked := revokeOutcome.Value

	if !created.Created {
		t.Fatalf("pool-machine creation result = %+v, want a new machine", created)
	}
	if len(revoked.Machines) != 1 || revoked.Machines[0].ID != created.Machine.Machine.ID {
		t.Fatalf("revoked machines = %+v, want created machine %s", revoked.Machines, created.Machine.Machine.ID)
	}
	assertPoolMachineRevokedAfterConcurrentCreate(
		t,
		ctx,
		fixture,
		created.Machine.Machine.ID,
	)
}

func TestMultiPoolLaunchLocksEveryPoolBeforeAnyGrant(t *testing.T) {
	t.Parallel()
	for _, reverse := range []bool{false, true} {
		name := "forward source order"
		slug := "forward"
		if reverse {
			name = "reverse source order"
			slug = "reverse"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMachineLifecycleLockOrderFixture(t, ctx, "multi-pool-"+slug)
			secondPool := createLaunchTestMachinePool(
				t,
				ctx,
				fixture.store,
				"Multi Pool "+slug,
				"test.provider",
				defaultMachineFieldsForTest{
					DefaultMachineProviderOptions: json.RawMessage(`{"image":"multi-pool"}`),
				},
				2,
				fixture.now.Add(6*time.Second),
			)
			secondGrant, err := fixture.store.Execution().CreateProjectMachinePoolGrant(
				ctx,
				executionstore.CreateProjectMachinePoolGrantInput{
					OrgID:          testOrgID,
					ProjectID:      testProjectID,
					MachinePoolID:  secondPool.ID,
					IdempotencyKey: "multi-pool-grant-" + slug,
				},
			)
			if err != nil {
				t.Fatalf("create second pool grant: %v", err)
			}
			type source struct {
				pool  executionstore.MachinePoolRecord
				grant executionstore.ProjectMachinePoolGrantRecord
			}
			sources := []source{
				{pool: fixture.machinePool, grant: fixture.poolGrant},
				{pool: secondPool, grant: secondGrant},
			}
			if reverse {
				sources[0], sources[1] = sources[1], sources[0]
			}
			profile := mustCreateConfigAndProfileBookmarkFromYAML(
				t,
				ctx,
				fixture.store,
				"multi-pool-profile-"+slug,
				"Multi Pool "+name,
				fmt.Sprintf(`
instruction: Validate pool lock classes.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: %s
    max_machines: 1
    initial_num_machines: 0
  - machine_pool_name: %s
    max_machines: 1
    initial_num_machines: 0
tools:
  run_command: {}
`, sources[0].pool.Name, sources[1].pool.Name),
			)

			controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
			controlQ := dbsqlc.New(controlTx)
			if _, err := controlQ.LockMachinePoolForLifecycle(
				ctx,
				dbsqlc.LockMachinePoolForLifecycleParams{
					OrgID: testOrgID,
					ID:    sources[1].pool.ID,
				},
			); err != nil {
				t.Fatalf("lock second source pool: %v", err)
			}

			launchDone := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
				return fixture.store.Execution().IntegrationLaunchAgentOnce(
					context.Background(),
					executionstore.LaunchAgentInput{
						ProjectID:      testProjectID,
						ProfileID:      profile.ID,
						AgentConfigID:  profile.CurrentConfigID,
						LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
						IdempotencyKey: "multi-pool-launch-" + slug,
					},
				)
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 1)

			grantLockCtx, cancelGrantLock := context.WithTimeout(ctx, 2*time.Second)
			defer cancelGrantLock()
			if _, err := controlQ.LockProjectMachinePoolGrantForLifecycle(
				grantLockCtx,
				dbsqlc.LockProjectMachinePoolGrantForLifecycleParams{
					ID: sources[0].grant.ID,
				},
			); err != nil {
				t.Fatalf("lock first source grant while launch waits for second pool: %v", err)
			}
			if err := controlTx.Commit(ctx); err != nil {
				t.Fatalf("release multi-pool control transaction: %v", err)
			}

			outcome := integrationdb.Await(t, launchDone, "multi-pool launch")
			if outcome.Err != nil {
				t.Fatalf("launch multi-pool agent: %v", outcome.Err)
			}
		})
	}
}

func TestDeletePoolMachineLocksMachineBeforeAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "tool-delete")
	created, err := fixture.createMachineOnce(ctx)
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: created.Machine.Machine.ID},
	); err != nil {
		t.Fatalf("lock machine for lifecycle order: %v", err)
	}

	deleteDone := integrationdb.RunAsync(func() (executionstore.PoolMachineRecord, error) {
		return executeToolCallOnceForLockOrder[executionstore.PoolMachineRecord](
			ctx,
			fixture.store,
			fixture.transaction(fixture.deleteToolCallID),
			executionstore.DeletePoolMachineForToolCall(
				executionstore.DeletePoolMachineInput{MachineID: created.Machine.Binding.MachineID},
				acceptedPoolMachineCompletionForTest,
			),
		)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)

	agentLockCtx, cancelAgentLock := context.WithTimeout(ctx, 2*time.Second)
	defer cancelAgentLock()
	if _, err := controlQ.LockAgentInProject(
		agentLockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent while tool deletion waits for machine: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release machine lock control transaction: %v", err)
	}

	deleteOutcome := integrationdb.Await(t, deleteDone, "pool-machine tool deletion")
	if deleteOutcome.Err != nil {
		t.Fatalf("delete pool machine in one transaction attempt: %v", deleteOutcome.Err)
	}
	if deleteOutcome.Value.Machine.LifecycleState != "deleting" ||
		deleteOutcome.Value.Machine.LifecycleReasonCode != "machine_tool_delete" ||
		deleteOutcome.Value.Binding.DeleteToolCallID != fixture.deleteToolCallID {
		t.Fatalf("deleted pool machine = %+v", deleteOutcome.Value)
	}
}

func TestMachineDeletionLocksAllTerminalWorkAgentsInStableOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal-agent-order")
	secondAgentID := mustCreateAgent(t, ctx, fixture.Store)
	secondBinding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		fixture.Store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               secondAgentID,
			ProjectMachineGrantID: fixture.GrantID,
			BindingKind:           "explicit",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("bind second agent to machine: %v", err)
	}
	secondLock, err := fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		secondAgentID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire second agent runtime lock: %v", err)
	}
	second := fixture
	second.AgentID = secondAgentID
	second.BindingID = secondBinding.ID
	second.Lock = secondLock
	lower, higher := fixture, second
	if higher.AgentID.String() < lower.AgentID.String() {
		lower, higher = higher, lower
	}

	higherToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		higher,
		"terminal-agent-order-running",
		"run_command",
	)
	higherProcess, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       higher.AgentID,
		ToolCallID:    higherToolCallID,
		RuntimeLockID: higher.Lock.ID,
	}, executionstore.CreateProcessInput{
		ExecutionSpec:         processcmd.ForShell("sleep 3600", "sh", ""),
		AgentMachineBindingID: higher.BindingID,
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start higher-ID process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		higherProcess.ID,
	); err != nil || !found {
		t.Fatalf("accept higher-ID process found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, higher, higherProcess, fixture.Now)

	lowerToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		lower,
		"terminal-agent-order-queued",
		"run_command",
	)
	lowerProcess, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       lower.AgentID,
		ToolCallID:    lowerToolCallID,
		RuntimeLockID: lower.Lock.ID,
	}, executionstore.CreateProcessInput{
		ExecutionSpec:         processcmd.ForShell("sleep 3600", "sh", ""),
		AgentMachineBindingID: lower.BindingID,
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("queue lower-ID process: %v", err)
	}

	controlTx := integrationdb.BeginTx(t, ctx, fixture.Store.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: lower.AgentID},
	); err != nil {
		t.Fatalf("lock lower-ID agent: %v", err)
	}

	deleteDone := integrationdb.RunAsyncError(func() error {
		_, deleteErr := fixture.Store.Execution().IntegrationDeleteMachineOnce(
			context.Background(),
			executionstore.DeleteMachineInput{OrgID: fixture.OrgID, MachineID: fixture.MachineID},
		)
		return deleteErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "LockAgentInProject", 1)

	probeCtx, cancelProbe := context.WithTimeout(ctx, 2*time.Second)
	defer cancelProbe()
	if _, err := controlQ.LockAgentInProject(
		probeCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: higher.AgentID},
	); err != nil {
		t.Fatalf("lock higher-ID agent while deletion waits for lower-ID agent: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release agent-order control transaction: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "machine deletion"); err != nil {
		t.Fatalf("delete machine in one transaction attempt: %v", err)
	}

	currentHigher, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		higher.AgentID,
		higherProcess.ID,
	)
	if err != nil {
		t.Fatalf("load higher-ID process: %v", err)
	}
	if currentHigher.State != executionstore.ProcessStateUnknown {
		t.Fatalf("higher-ID process state = %q, want unknown", currentHigher.State)
	}
	currentLower, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		lower.AgentID,
		lowerProcess.ID,
	)
	if err != nil {
		t.Fatalf("load lower-ID process: %v", err)
	}
	if currentLower.State != executionstore.ProcessStateFailed {
		t.Fatalf("lower-ID process state = %q, want failed", currentLower.State)
	}
}

func TestCompletePoolMachineDeletionLocksPoolBeforeMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "complete-delete")
	created, err := fixture.createMachineOnce(ctx)
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}
	deleted, err := executeToolCallOnceForLockOrder[executionstore.PoolMachineRecord](
		ctx,
		fixture.store,
		fixture.transaction(fixture.deleteToolCallID),
		executionstore.DeletePoolMachineForToolCall(
			executionstore.DeletePoolMachineInput{MachineID: created.Machine.Binding.MachineID},
			acceptedPoolMachineCompletionForTest,
		),
	)
	if err != nil {
		t.Fatalf("request pool machine deletion: %v", err)
	}
	deleting, claimed, err := fixture.store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                deleted.Machine.ID,
			LifecycleReasonCode:      "machine_tool_delete",
			LifecycleReasonMessage:   "deleted by machine tool",
			ExpectedLifecycleVersion: deleted.Machine.LifecycleVersion,
		},
	)
	if err != nil || !claimed {
		t.Fatalf("claim pool machine deletion claimed=%v err=%v", claimed, err)
	}

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachinePoolForLifecycle(
		ctx,
		dbsqlc.LockMachinePoolForLifecycleParams{OrgID: testOrgID, ID: fixture.machinePool.ID},
	); err != nil {
		t.Fatalf("lock pool before deletion completion: %v", err)
	}

	completeDone := integrationdb.RunAsyncError(func() error {
		return fixture.store.Execution().CompletePoolMachineDeletion(
			ctx,
			testOrgID,
			deleted.Machine.ID,
			deleting.Machine.DeleteAttempts,
		)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 1)

	machineLockCtx, cancelMachineLock := context.WithTimeout(ctx, 2*time.Second)
	defer cancelMachineLock()
	if _, err := controlQ.LockMachineForLifecycle(
		machineLockCtx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: deleted.Machine.ID},
	); err != nil {
		t.Fatalf("lock machine while deletion completion waits for pool: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release pool lock control transaction: %v", err)
	}
	if err := integrationdb.Await(t, completeDone, "pool-machine deletion completion"); err != nil {
		t.Fatalf("complete pool machine deletion: %v", err)
	}
	current, err := fixture.store.Execution().GetMachine(ctx, testOrgID, deleted.Machine.ID)
	if err != nil {
		t.Fatalf("load completed pool-machine deletion: %v", err)
	}
	if current.LifecycleState != "deleted" || current.DeletedAt == nil {
		t.Fatalf("completed pool-machine deletion = %+v", current)
	}
}

func TestBYODaemonTokenCreationSerializesWithMachineDeletion(t *testing.T) {
	t.Parallel()
	for _, tokenWins := range []bool{true, false} {
		name := "deletion wins"
		slug := "deletion-wins"
		if tokenWins {
			name = "token wins"
			slug = "token-wins"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMachineLifecycleLockOrderFixture(t, ctx, "daemon-token-delete-"+slug)
			machine, err := fixture.store.Execution().CreateDaemonMachine(
				ctx,
				executionstore.CreateDaemonMachineInput{
					OrgID:          testOrgID,
					DisplayName:    "Daemon Token Deletion",
					IdempotencyKey: "daemon-token-delete-" + slug,
				},
			)
			if err != nil {
				t.Fatalf("create BYO machine: %v", err)
			}

			controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
			if _, err := dbsqlc.New(controlTx).LockMachineForLifecycle(
				ctx,
				dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: machine.ID},
			); err != nil {
				t.Fatalf("lock machine for daemon token contention: %v", err)
			}

			var tokenDone, deleteDone <-chan error
			startToken := func() {
				tokenDone = integrationdb.RunAsyncError(func() error {
					_, tokenErr := fixture.store.Execution().CreateBYOMachineDaemonToken(
						context.Background(),
						executionstore.CreateBYOMachineDaemonTokenInput{
							OrgID:     testOrgID,
							MachineID: machine.ID,
							Name:      "contention token",
						},
					)
					return tokenErr
				})
			}
			startDelete := func() {
				deleteDone = integrationdb.RunAsyncError(func() error {
					_, deleteErr := fixture.store.Execution().IntegrationDeleteMachineOnce(
						context.Background(),
						executionstore.DeleteMachineInput{OrgID: testOrgID, MachineID: machine.ID},
					)
					return deleteErr
				})
			}
			if tokenWins {
				startToken()
				integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)
				startDelete()
			} else {
				startDelete()
				integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)
				startToken()
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 2)
			if err := controlTx.Commit(ctx); err != nil {
				t.Fatalf("release machine lock control transaction: %v", err)
			}

			tokenErr := integrationdb.Await(t, tokenDone, "daemon token creation")
			if tokenWins && tokenErr != nil {
				t.Fatalf("create daemon token before deletion: %v", tokenErr)
			}
			if !tokenWins && !errors.Is(tokenErr, storeerr.ErrNotFound) {
				t.Fatalf("create daemon token after deletion error = %v, want not found", tokenErr)
			}
			if err := integrationdb.Await(t, deleteDone, "BYO machine deletion"); err != nil {
				t.Fatalf("delete BYO machine: %v", err)
			}

			var tokenCount, revokedCount int
			if err := fixture.pool.QueryRow(
				ctx,
				`SELECT count(*)::integer,
				        count(*) FILTER (WHERE revoked_at IS NOT NULL)::integer
				 FROM machine_daemon_tokens
				 WHERE org_id = $1 AND machine_id = $2`,
				testOrgID,
				machine.ID,
			).Scan(&tokenCount, &revokedCount); err != nil {
				t.Fatalf("count daemon tokens after deletion: %v", err)
			}
			if tokenWins && (tokenCount != 1 || revokedCount != 1) {
				t.Fatalf("daemon token counts = total %d revoked %d, want 1 and 1", tokenCount, revokedCount)
			}
			if !tokenWins && tokenCount != 0 {
				t.Fatalf("daemon token count after deletion won = %d, want 0", tokenCount)
			}
		})
	}
}

func TestProjectAndMachineDeletionSerializeOnExplicitMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "project-machine-delete")
	machine, err := fixture.store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Project Deletion Explicit Machine",
			IdempotencyKey: "project-deletion-explicit-machine",
		},
	)
	if err != nil {
		t.Fatalf("create explicit machine: %v", err)
	}
	grant, _, err := fixture.store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "project-deletion-explicit-grant",
		},
	)
	if err != nil {
		t.Fatalf("create explicit machine grant: %v", err)
	}
	binding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		fixture.store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               fixture.agent.ID,
			ProjectMachineGrantID: grant.ID,
			BindingKind:           executionstore.MachineBindingKindExplicit,
		},
	)
	if err != nil {
		t.Fatalf("bind explicit machine: %v", err)
	}
	actor, err := executionstore.OmnaraActorParams(
		testOrgID,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
	)
	if err != nil {
		t.Fatalf("build project deletion actor: %v", err)
	}

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: machine.ID},
	); err != nil {
		t.Fatalf("lock explicit machine for lifecycle order: %v", err)
	}

	projectDeleteDone := integrationdb.RunAsyncError(func() error {
		_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
			ctx,
			testOrgID,
			testProjectID,
			actor,
		)
		return deleteErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)

	machineDeleteDone := integrationdb.RunAsyncError(func() error {
		_, deleteErr := fixture.store.Execution().IntegrationDeleteMachineOnce(
			ctx,
			executionstore.DeleteMachineInput{OrgID: testOrgID, MachineID: machine.ID},
		)
		return deleteErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release explicit machine lock control transaction: %v", err)
	}
	for label, done := range map[string]<-chan error{
		"project deletion": projectDeleteDone,
		"machine deletion": machineDeleteDone,
	} {
		if err := integrationdb.Await(t, done, label); err != nil {
			t.Fatalf("%s after machine lock release: %v", label, err)
		}
	}

	if _, err := fixture.store.Identity().GetProject(ctx, testOrgID, testProjectID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted project lookup error = %v, want not found", err)
	}
	deletedMachine, err := fixture.store.Execution().GetMachine(ctx, testOrgID, machine.ID)
	if err != nil {
		t.Fatalf("load deleted explicit machine: %v", err)
	}
	if deletedMachine.LifecycleState != "deleted" || deletedMachine.DeletedAt == nil {
		t.Fatalf("deleted explicit machine = %+v", deletedMachine)
	}
	var grantCount int
	var bindingState string
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*)::integer FROM project_machine_grants WHERE id = $1),
		   state
		 FROM agent_machine_bindings
		 WHERE id = $2`,
		grant.ID,
		binding.ID,
	).Scan(&grantCount, &bindingState); err != nil {
		t.Fatalf("load explicit relationship outcome: %v", err)
	}
	if grantCount != 0 || bindingState != "released" {
		t.Fatalf(
			"explicit relationship outcome: grants=%d binding_state=%q",
			grantCount,
			bindingState,
		)
	}
}

func TestAgentLaunchEntersProjectLifecycleBeforeLockingProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"project-profile-order@example.com",
		"Project Profile Order",
	)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"project-profile-order",
		"Project Profile Order",
		`
instruction: Verify project lifecycle admission precedes profile locking.
model:
  provider_config: openai-prod
  name: gpt-test
`,
	)

	controlTx := integrationdb.BeginTx(t, ctx, pool)
	controlQ := dbsqlc.New(controlTx)
	if err := controlQ.LockProjectLifecycleExclusive(
		ctx,
		dbsqlc.LockProjectLifecycleExclusiveParams{ProjectID: testProjectID},
	); err != nil {
		t.Fatalf("lock project lifecycle exclusively: %v", err)
	}

	launchDone := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID},
			IdempotencyKey: "project-profile-order",
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleShared", 1)

	profileLockCtx, cancelProfileLock := context.WithTimeout(ctx, 2*time.Second)
	defer cancelProfileLock()
	if _, err := controlQ.LockAgentProfile(
		profileLockCtx,
		dbsqlc.LockAgentProfileParams{ProjectID: testProjectID, ProfileID: profile.ID},
	); err != nil {
		t.Fatalf("lock profile while launch waits for project lifecycle: %v", err)
	}
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release project lifecycle control transaction: %v", err)
	}

	outcome := integrationdb.Await(t, launchDone, "agent launch")
	if outcome.Err != nil {
		t.Fatalf("launch after project lifecycle release: %v", outcome.Err)
	}
	if !outcome.Value.Created {
		t.Fatalf("launch result = %+v, want a new agent", outcome.Value)
	}
}

func TestLaunchAndConfigReconciliationSerializePoolBeforeMachineEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "launch-config-environment")
	machine, err := fixture.store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Shared Environment Machine",
			IdempotencyKey: "shared-environment-machine",
		},
	)
	if err != nil {
		t.Fatalf("create shared environment machine: %v", err)
	}
	if _, _, err := fixture.store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "shared-environment-grant",
		},
	); err != nil {
		t.Fatalf("grant shared environment machine: %v", err)
	}
	sourceYAML := fmt.Sprintf(`
instruction: Exercise machine source lock ordering.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: %s
  - machine_pool_name: %s
    max_machines: 1
    initial_num_machines: 0
tools:
  run_command: {}
`, machine.DisplayName, fixture.machinePool.Name)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		fixture.store,
		"shared-environment-launch",
		"Shared Environment Launch",
		sourceYAML,
	)
	nextConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		fixture.store,

		sourceYAML,
	)

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: testOrgID, ID: fixture.machinePool.ID},
	); err != nil {
		t.Fatalf("lock shared machine pool: %v", err)
	}

	launchDone := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
		return fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
			IdempotencyKey: "shared-environment-launch",
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 1)

	environmentCtx, cancelEnvironment := context.WithTimeout(ctx, 2*time.Second)
	defer cancelEnvironment()
	if err := controlQ.LockMachineEnvironmentKey(
		environmentCtx,
		dbsqlc.LockMachineEnvironmentKeyParams{MachineID: machine.ID},
	); err != nil {
		t.Fatalf("lock machine environment while launch waits for pool: %v", err)
	}

	configDone := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
		return fixture.store.Execution().IntegrationChangeAgentConfigOnce(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: changeInputFromRecord(nextConfig),
			AgentID:                fixture.agent.ID,
			ActorType:              identitystore.PrincipalTypeUser,
			ActorID:                fixture.userID,
			IdempotencyKey:         "shared-environment-change",
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release pool and environment lock control transaction: %v", err)
	}
	launchOutcome := integrationdb.Await(t, launchDone, "launch")
	if launchOutcome.Err != nil {
		t.Fatalf("launch after pool lock release: %v", launchOutcome.Err)
	}
	launched := launchOutcome.Value
	configOutcome := integrationdb.Await(t, configDone, "configuration reconciliation")
	if configOutcome.Err != nil {
		t.Fatalf("configuration reconciliation after pool lock release: %v", configOutcome.Err)
	}
	changed := configOutcome.Value

	if len(launched.MachineBindings) != 1 || launched.MachineBindings[0].MachineID != machine.ID {
		t.Fatalf("launched machine bindings = %+v, want explicit machine %s", launched.MachineBindings, machine.ID)
	}
	if changed.AgentConfig.ID != nextConfig.ID {
		t.Fatalf("changed config = %s, want %s", changed.AgentConfig.ID, nextConfig.ID)
	}
	if _, err := fixture.store.q.GetAgentMachineBindingByMachine(
		ctx,
		dbsqlc.GetAgentMachineBindingByMachineParams{
			ProjectID:   testProjectID,
			AgentID:     fixture.agent.ID,
			MachineID:   machine.ID,
			BindingKind: string(executionstore.MachineBindingKindExplicit),
		},
	); err != nil {
		t.Fatalf("load reconciled explicit machine binding: %v", err)
	}
}

func TestAgentLaunchAndExplicitGrantRevocationSerialize(t *testing.T) {
	t.Parallel()
	t.Run("launch wins", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "explicit-launch-wins")
		explicit := createExplicitGrantLifecycleFixture(t, ctx, fixture, "explicit-launch-wins")
		profile := mustCreateConfigAndProfileBookmarkFromYAML(
			t,
			ctx,
			fixture.store,
			"explicit-launch-wins",
			"Explicit Launch Wins",
			explicit.configYAML,
		)

		controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
		if err := dbsqlc.New(controlTx).LockMachineEnvironmentKey(
			ctx,
			dbsqlc.LockMachineEnvironmentKeyParams{MachineID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine environment: %v", err)
		}

		launchDone := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
			return fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
				ProjectID:      testProjectID,
				ProfileID:      profile.ID,
				AgentConfigID:  profile.CurrentConfigID,
				LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
				IdempotencyKey: "explicit-launch-wins",
			})
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineEnvironmentKey", 1)

		revokeDone := integrationdb.RunAsync(func() (executionstore.ProjectMachineGrantRecord, error) {
			return fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit launch control transaction: %v", err)
		}

		launched := integrationdb.AwaitSuccess(t, launchDone, "launch in one transaction attempt")
		if !launched.Created || len(launched.MachineBindings) != 1 ||
			launched.MachineBindings[0].MachineID != explicit.machine.ID {
			t.Fatalf("launch result = %+v", launched)
		}
		revoked := integrationdb.AwaitSuccess(t, revokeDone, "grant revocation in one transaction attempt")
		if revoked.ID != explicit.grant.ID {
			t.Fatalf("revoked grant = %s, want %s", revoked.ID, explicit.grant.ID)
		}
		assertExplicitGrantLifecycleOutcome(
			t,
			ctx,
			fixture,
			explicit,
			launched.Agent.ID,
			0,
			1,
		)
	})

	t.Run("revocation wins", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "explicit-revoke-launch")
		explicit := createExplicitGrantLifecycleFixture(t, ctx, fixture, "explicit-revoke-launch")
		profile := mustCreateConfigAndProfileBookmarkFromYAML(
			t,
			ctx,
			fixture.store,
			"explicit-revoke-launch",
			"Explicit Revoke Launch",
			explicit.configYAML,
		)

		controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
		if _, err := dbsqlc.New(controlTx).LockMachineForLifecycle(
			ctx,
			dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine: %v", err)
		}

		revokeDone := integrationdb.RunAsync(func() (executionstore.ProjectMachineGrantRecord, error) {
			return fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)

		launchDone := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
			return fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
				ProjectID:      testProjectID,
				ProfileID:      profile.ID,
				AgentConfigID:  profile.CurrentConfigID,
				LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
				IdempotencyKey: "explicit-revoke-launch",
			})
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 2)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit revocation control transaction: %v", err)
		}

		integrationdb.AwaitSuccess(t, revokeDone, "grant revocation in one transaction attempt")
		if outcome := integrationdb.Await(t, launchDone, "rejected launch"); !errors.Is(outcome.Err, storeerr.ErrNotFound) {
			t.Fatalf("launch after revocation error = %v, want not found", outcome.Err)
		}
		assertExplicitGrantLifecycleOutcome(t, ctx, fixture, explicit, uuid.Nil, 0, 0)
		var agentCount int
		if err := fixture.pool.QueryRow(
			ctx,
			`SELECT count(*)::integer FROM agents WHERE project_id = $1 AND idempotency_key = $2`,
			testProjectID,
			"explicit-revoke-launch",
		).Scan(&agentCount); err != nil {
			t.Fatalf("count rejected launch agents: %v", err)
		}
		if agentCount != 0 {
			t.Fatalf("rejected launch agents = %d, want zero", agentCount)
		}
	})
}

func TestConfigReconciliationAndExplicitGrantRevocationSerialize(t *testing.T) {
	t.Parallel()
	t.Run("configuration wins", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "explicit-config-wins")
		explicit := createExplicitGrantLifecycleFixture(t, ctx, fixture, "explicit-config-wins")
		nextConfig := mustCreateAgentConfigFromYAML(
			t,
			ctx,
			fixture.store,

			explicit.configYAML,
		)

		controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
		if err := dbsqlc.New(controlTx).LockMachineEnvironmentKey(
			ctx,
			dbsqlc.LockMachineEnvironmentKeyParams{MachineID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine environment: %v", err)
		}

		configDone := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
			return fixture.store.Execution().IntegrationChangeAgentConfigOnce(
				ctx,
				executionstore.ChangeAgentConfigInput{
					CreateAgentConfigInput: changeInputFromRecord(nextConfig),
					AgentID:                fixture.agent.ID,
					ActorType:              identitystore.PrincipalTypeUser,
					ActorID:                fixture.userID,
					IdempotencyKey:         "explicit-config-wins",
				},
			)
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineEnvironmentKey", 1)

		revokeDone := integrationdb.RunAsync(func() (executionstore.ProjectMachineGrantRecord, error) {
			return fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit config control transaction: %v", err)
		}

		changed := integrationdb.AwaitSuccess(t, configDone, "config change in one transaction attempt")
		if changed.AgentConfig.ID != nextConfig.ID {
			t.Fatalf("activated config = %s, want %s", changed.AgentConfig.ID, nextConfig.ID)
		}
		integrationdb.AwaitSuccess(t, revokeDone, "grant revocation in one transaction attempt")
		assertExplicitGrantLifecycleOutcome(t, ctx, fixture, explicit, fixture.agent.ID, 0, 1)
		assertAgentCurrentConfig(t, ctx, fixture, fixture.agent.ID, nextConfig.ID)
	})

	t.Run("revocation wins", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "explicit-revoke-config")
		explicit := createExplicitGrantLifecycleFixture(t, ctx, fixture, "explicit-revoke-config")
		nextConfig := mustCreateAgentConfigFromYAML(
			t,
			ctx,
			fixture.store,

			explicit.configYAML,
		)

		controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
		if _, err := dbsqlc.New(controlTx).LockMachineForLifecycle(
			ctx,
			dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine: %v", err)
		}

		revokeDone := integrationdb.RunAsync(func() (executionstore.ProjectMachineGrantRecord, error) {
			return fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)

		configDone := integrationdb.RunAsync(func() (executionstore.ChangeAgentConfigResult, error) {
			return fixture.store.Execution().IntegrationChangeAgentConfigOnce(
				ctx,
				executionstore.ChangeAgentConfigInput{
					CreateAgentConfigInput: changeInputFromRecord(nextConfig),
					AgentID:                fixture.agent.ID,
					ActorType:              identitystore.PrincipalTypeUser,
					ActorID:                fixture.userID,
					IdempotencyKey:         "explicit-revoke-config",
				},
			)
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 2)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit config revocation control transaction: %v", err)
		}

		integrationdb.AwaitSuccess(t, revokeDone, "grant revocation in one transaction attempt")
		if outcome := integrationdb.Await(
			t, configDone, "rejected config change",
		); !errors.Is(outcome.Err, storeerr.ErrNotFound) {
			t.Fatalf("config change after revocation error = %v, want not found", outcome.Err)
		}
		assertExplicitGrantLifecycleOutcome(t, ctx, fixture, explicit, fixture.agent.ID, 0, 0)
		assertAgentCurrentConfig(t, ctx, fixture, fixture.agent.ID, fixture.agent.CurrentConfigID)
	})
}

type explicitGrantLifecycleFixture struct {
	machine    executionstore.MachineRecord
	grant      executionstore.ProjectMachineGrantRecord
	configYAML string
}

func createExplicitGrantLifecycleFixture(
	t *testing.T,
	ctx context.Context,
	fixture machineLifecycleLockOrderFixture,
	label string,
) explicitGrantLifecycleFixture {
	t.Helper()
	machine, err := fixture.store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Explicit Lifecycle " + label,
		IdempotencyKey: "explicit-lifecycle-machine-" + label,
	})
	if err != nil {
		t.Fatalf("create explicit lifecycle machine: %v", err)
	}
	grant, _, err := fixture.store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "explicit-lifecycle-grant-" + label,
		},
	)
	if err != nil {
		t.Fatalf("create explicit lifecycle grant: %v", err)
	}
	return explicitGrantLifecycleFixture{
		machine: machine,
		grant:   grant,
		configYAML: fmt.Sprintf(`
instruction: Exercise explicit machine lifecycle admission.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: %s
tools:
  run_command: {}
`, machine.DisplayName),
	}
}

func assertExplicitGrantLifecycleOutcome(
	t *testing.T,
	ctx context.Context,
	fixture machineLifecycleLockOrderFixture,
	explicit explicitGrantLifecycleFixture,
	agentID uuid.UUID,
	wantGrants, wantBindings int,
) {
	t.Helper()
	var grantCount, bindingCount int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*)::integer FROM project_machine_grants WHERE id = $1),
		   (SELECT count(*)::integer
		    FROM agent_machine_bindings
		    WHERE project_id = $2
		      AND machine_id = $3
		      AND ($4::uuid = '00000000-0000-0000-0000-000000000000' OR agent_id = $4)
		      AND state = 'attached')`,
		explicit.grant.ID,
		testProjectID,
		explicit.machine.ID,
		agentID,
	).Scan(&grantCount, &bindingCount); err != nil {
		t.Fatalf("load explicit grant lifecycle outcome: %v", err)
	}
	if grantCount != wantGrants || bindingCount != wantBindings {
		t.Fatalf(
			"explicit lifecycle outcome grants=%d bindings=%d, want grants=%d bindings=%d",
			grantCount,
			bindingCount,
			wantGrants,
			wantBindings,
		)
	}
}

func assertAgentCurrentConfig(
	t *testing.T,
	ctx context.Context,
	fixture machineLifecycleLockOrderFixture,
	agentID, wantConfigID uuid.UUID,
) {
	t.Helper()
	var configID uuid.UUID
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT current_config_id FROM agents WHERE project_id = $1 AND id = $2`,
		testProjectID,
		agentID,
	).Scan(&configID); err != nil {
		t.Fatalf("load agent current config: %v", err)
	}
	if configID != wantConfigID {
		t.Fatalf("agent current config = %s, want %s", configID, wantConfigID)
	}
}

func TestProjectDeletionWaitsForConcurrentAgentArchive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := runScopeDeletionAfterConcurrentAgentArchive(
		t,
		ctx,
		"project-archive",
		"LockProjectLifecycleExclusive",
		func(fixture machineLifecycleLockOrderFixture, actor *executionstore.ActorParams) error {
			_, err := fixture.store.Organizations().DeleteProjectOnceForIntegration(
				ctx,
				testOrgID,
				testProjectID,
				actor,
			)
			return err
		},
	)
	if _, err := fixture.store.Identity().GetProject(ctx, testOrgID, testProjectID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted project lookup error = %v, want not found", err)
	}
}

func TestOrganizationDeletionWaitsForConcurrentAgentArchive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := runScopeDeletionAfterConcurrentAgentArchive(
		t,
		ctx,
		"organization-archive",
		"LockOrganizationLifecycleExclusive",
		func(fixture machineLifecycleLockOrderFixture, actor *executionstore.ActorParams) error {
			_, err := fixture.store.Organizations().DeleteOrganizationOnceForIntegration(
				ctx,
				testOrgID,
				actor,
			)
			return err
		},
	)
	if _, err := fixture.store.Identity().GetOrg(ctx, testOrgID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted organization lookup error = %v, want not found", err)
	}
}

func TestPoolMachineToolOperationsEnterProjectLifecycle(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"create", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newMachineLifecycleLockOrderFixture(t, ctx, "project-tool-"+operation)
			var runOperation func() error
			if operation == "create" {
				runOperation = func() error {
					_, err := fixture.createMachineOnce(ctx)
					return err
				}
			} else {
				created, err := fixture.createMachineOnce(ctx)
				if err != nil {
					t.Fatalf("create machine before delete operation: %v", err)
				}
				runOperation = func() error {
					_, err := executeToolCallOnceForLockOrder[executionstore.PoolMachineRecord](
						ctx,
						fixture.store,
						fixture.transaction(fixture.deleteToolCallID),
						executionstore.DeletePoolMachineForToolCall(
							executionstore.DeletePoolMachineInput{MachineID: created.Machine.Binding.MachineID},
							acceptedPoolMachineCompletionForTest,
						),
					)
					return err
				}
			}

			controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
			if _, err := dbsqlc.New(controlTx).LockAgentInProject(
				ctx,
				dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
			); err != nil {
				t.Fatalf("lock agent for project deletion: %v", err)
			}

			actor := scopeDeletionActor(t, fixture)
			deleteDone := integrationdb.RunAsyncError(func() error {
				_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
					context.Background(),
					testOrgID,
					testProjectID,
					actor,
				)
				return deleteErr
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)

			operationDone := integrationdb.RunAsyncError(runOperation)
			integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 1)
			if err := controlTx.Commit(ctx); err != nil {
				t.Fatalf("release project tool control transaction: %v", err)
			}
			if err := integrationdb.Await(t, deleteDone, "project deletion"); err != nil {
				t.Fatalf("delete project: %v", err)
			}
			if err := integrationdb.Await(t, operationDone, "pool machine "+operation); !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("%s pool machine after project deletion error = %v, want not found", operation, err)
			}
		})
	}
}

func TestProjectGrantCreationWaitingBehindDeletionRejectsInactiveProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "project-admission")
	targetPool := createLaunchTestMachinePool(
		t,
		ctx,
		fixture.store,
		"Project Admission Target",
		"test.provider",
		defaultMachineFieldsForTest{
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"project-admission"}`),
		},
		2,
		fixture.now.Add(6*time.Second),
	)
	targetMachine, err := fixture.store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Project Admission Target",
			IdempotencyKey: "project-admission-target",
		},
	)
	if err != nil {
		t.Fatalf("create project admission target machine: %v", err)
	}

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent for project deletion: %v", err)
	}

	actor := scopeDeletionActor(t, fixture)
	deleteDone := integrationdb.RunAsyncError(func() error {
		_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
			ctx,
			testOrgID,
			testProjectID,
			actor,
		)
		return deleteErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)

	poolGrantDone := integrationdb.RunAsyncError(func() error {
		_, createErr := fixture.store.Execution().CreateProjectMachinePoolGrant(
			ctx,
			executionstore.CreateProjectMachinePoolGrantInput{
				OrgID:          testOrgID,
				ProjectID:      testProjectID,
				MachinePoolID:  targetPool.ID,
				IdempotencyKey: "project-admission-pool-grant",
			},
		)
		return createErr
	})
	explicitGrantDone := integrationdb.RunAsyncError(func() error {
		_, _, createErr := fixture.store.Execution().CreateProjectMachineGrant(
			ctx,
			executionstore.CreateProjectMachineGrantInput{
				OrgID:          testOrgID,
				ProjectID:      testProjectID,
				MachineID:      targetMachine.ID,
				IdempotencyKey: "project-admission-explicit-grant",
			},
		)
		return createErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release source lock control transaction: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "project deletion"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	for label, done := range map[string]<-chan error{
		"machine-pool grant": poolGrantDone,
		"explicit grant":     explicitGrantDone,
	} {
		if err := integrationdb.Await(t, done, label+" creation"); !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("%s creation after project deletion error = %v, want not found", label, err)
		}
	}

	var poolGrantCount, explicitGrantCount int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*)::int FROM project_machine_pool_grants
		    WHERE project_id = $1 AND machine_pool_id = $2),
		   (SELECT count(*)::int FROM project_machine_grants
		    WHERE project_id = $1 AND machine_id = $3)`,
		testProjectID,
		targetPool.ID,
		targetMachine.ID,
	).Scan(&poolGrantCount, &explicitGrantCount); err != nil {
		t.Fatalf("count relationships beneath deleted project: %v", err)
	}
	if poolGrantCount != 0 || explicitGrantCount != 0 {
		t.Fatalf(
			"relationships beneath deleted project: pool grants=%d explicit grants=%d",
			poolGrantCount,
			explicitGrantCount,
		)
	}
}

func TestMCPReconciliationWaitingBehindProjectDeletionRejectsInactiveProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "mcp-project-delete")

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	if _, err := dbsqlc.New(controlTx).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent for project deletion: %v", err)
	}

	actor := scopeDeletionActor(t, fixture)
	deleteDone := integrationdb.RunAsyncError(func() error {
		_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
			ctx,
			testOrgID,
			testProjectID,
			actor,
		)
		return deleteErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)

	reconcileDone := integrationdb.RunAsyncError(func() error {
		_, reconcileErr := fixture.store.Execution().ReconcileAgentMCPConnections(
			ctx,
			testProjectID,
			fixture.agent.ID,
			[]agentconfig.RuntimeMCPServer{{
				ServerKey: "project-delete-race",
				URL:       "https://example.com/mcp",
			}},
		)
		return reconcileErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 1)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release source lock control transaction: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "project deletion"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if err := integrationdb.Await(t, reconcileDone, "mcp reconciliation"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("mcp reconciliation after project deletion error = %v, want not found", err)
	}

	var connectionCount int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT count(*)::int
		 FROM agent_mcp_connections
		 WHERE agent_id = $1 AND server_key = 'project-delete-race'`,
		fixture.agent.ID,
	).Scan(&connectionCount); err != nil {
		t.Fatalf("count mcp connections after project deletion race: %v", err)
	}
	if connectionCount != 0 {
		t.Fatalf("mcp connections created beneath deleted project = %d, want 0", connectionCount)
	}
}

func TestMCPReconciliationWaitingBehindAgentArchiveRejectsArchivedAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "mcp-archive")

	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	if _, err := dbsqlc.New(controlTx).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent before archive: %v", err)
	}

	actor := scopeDeletionActor(t, fixture)
	archiveDone := integrationdb.RunAsyncError(func() error {
		_, _, archiveErr := fixture.store.Execution().IntegrationArchiveAgentOnce(
			ctx,
			testProjectID,
			fixture.agent.ID,
			actor,
		)
		return archiveErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)

	reconcileDone := integrationdb.RunAsyncError(func() error {
		_, reconcileErr := fixture.store.Execution().ReconcileAgentMCPConnections(
			ctx,
			testProjectID,
			fixture.agent.ID,
			[]agentconfig.RuntimeMCPServer{{
				ServerKey: "archive-race",
				URL:       "https://example.com/mcp",
			}},
		)
		return reconcileErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release agent lock control transaction: %v", err)
	}
	if err := integrationdb.Await(t, archiveDone, "agent archival"); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if err := integrationdb.Await(
		t, reconcileDone, "mcp reconciliation",
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("mcp reconciliation after archive error = %v, want state transition conflict", err)
	}

	var connectionCount int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT count(*)::int
		 FROM agent_mcp_connections
		 WHERE agent_id = $1 AND server_key = 'archive-race'`,
		fixture.agent.ID,
	).Scan(&connectionCount); err != nil {
		t.Fatalf("count mcp connections after archive race: %v", err)
	}
	if connectionCount != 0 {
		t.Fatalf("mcp connections created beneath archived agent = %d, want 0", connectionCount)
	}
}

func TestConfigChangeWaitingBehindAgentArchiveRejectsNewAdmission(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "config-archive")
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		fixture.store,

		`
instruction: Verify archived agents reject new config changes.
model:
  provider_config: openai-prod
  name: gpt-test
`,
	)
	acceptedInput := executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(config),
		AgentID:                fixture.agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                fixture.userID,
		IdempotencyKey:         "config-before-archive",
	}
	accepted, err := fixture.store.Execution().ChangeAgentConfig(ctx, acceptedInput)
	if err != nil {
		t.Fatalf("change config before archive: %v", err)
	}

	releaseArchive := startAgentArchiveBlockedAtAgent(t, ctx, fixture)

	rejectedInput := acceptedInput
	rejectedInput.IdempotencyKey = "config-after-archive"
	changeDone := integrationdb.RunAsyncError(func() error {
		_, changeErr := fixture.store.Execution().IntegrationChangeAgentConfigOnce(ctx, rejectedInput)
		return changeErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

	releaseArchive()
	if err := integrationdb.Await(
		t, changeDone, "rejected config change",
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("new config change after archive error = %v, want state transition conflict", err)
	}

	replayed, err := fixture.store.Execution().ChangeAgentConfig(ctx, acceptedInput)
	if err != nil {
		t.Fatalf("replay config change after archive: %v", err)
	}
	if replayed.ConfigChange.AgentInput.ID != accepted.ConfigChange.AgentInput.ID ||
		replayed.ConfigChange.Event.ID != accepted.ConfigChange.Event.ID {
		t.Fatalf("archived config replay = %+v, want %+v", replayed.ConfigChange, accepted.ConfigChange)
	}
	var state string
	var currentConfigID uuid.UUID
	var rejectedInputs int
	var hasWakeup bool
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT agent.state,
		        agent.current_config_id,
		        count(input.id)::integer,
		        EXISTS (SELECT 1 FROM agent_wakeups wake WHERE wake.agent_id = agent.id)
		 FROM agents agent
		 LEFT JOIN agent_inputs input ON input.agent_id = agent.id
		   AND input.idempotency_scope = 'agent_config_change'
		   AND input.input_idempotency_key = $2
		 WHERE agent.id = $1
		 GROUP BY agent.id`,
		fixture.agent.ID,
		rejectedInput.IdempotencyKey,
	).Scan(&state, &currentConfigID, &rejectedInputs, &hasWakeup); err != nil {
		t.Fatalf("load config archive outcome: %v", err)
	}
	if state != string(executionstore.AgentStateArchived) || currentConfigID != config.ID ||
		rejectedInputs != 0 || hasWakeup {
		t.Fatalf(
			"config archive outcome: state=%q current_config=%s rejected_inputs=%d has_wakeup=%t",
			state,
			currentConfigID,
			rejectedInputs,
			hasWakeup,
		)
	}
}

func TestPoolMachineCreationWaitingBehindAgentArchiveRejectsArchivedAgent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "pool-machine-archive")

	releaseArchive := startAgentArchiveBlockedAtAgent(t, ctx, fixture)

	createDone := integrationdb.RunAsyncError(func() error {
		_, createErr := fixture.createMachineOnce(ctx)
		return createErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

	releaseArchive()
	if err := integrationdb.Await(
		t, createDone, "rejected pool-machine creation",
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("pool-machine creation after archive error = %v, want state transition conflict", err)
	}

	var machineCount, bindingCount int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT
		   (SELECT count(*)::integer FROM machines WHERE machine_pool_id = $1),
		   (SELECT count(*)::integer FROM agent_machine_bindings
		    WHERE agent_id = $2 AND create_tool_call_id = $3)`,
		fixture.machinePool.ID,
		fixture.agent.ID,
		fixture.createToolCallID,
	).Scan(&machineCount, &bindingCount); err != nil {
		t.Fatalf("load pool-machine archive outcome: %v", err)
	}
	if machineCount != 0 || bindingCount != 0 {
		t.Fatalf("pool-machine archive outcome: machines=%d bindings=%d", machineCount, bindingCount)
	}
	toolCall, err := fixture.store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.agent.ID,
		fixture.createToolCallID,
	)
	if err != nil {
		t.Fatalf("load archived create-machine tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted {
		t.Fatalf("archived create-machine tool call state = %q, want completed", toolCall.State)
	}
}

func startAgentArchiveBlockedAtAgent(
	t *testing.T,
	ctx context.Context,
	fixture machineLifecycleLockOrderFixture,
) func() {
	t.Helper()
	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	if _, err := dbsqlc.New(controlTx).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent before archive: %v", err)
	}
	actor := scopeDeletionActor(t, fixture)
	archiveDone := integrationdb.RunAsyncError(func() error {
		_, _, archiveErr := fixture.store.Execution().IntegrationArchiveAgentOnce(
			ctx,
			testProjectID,
			fixture.agent.ID,
			actor,
		)
		return archiveErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)
	return func() {
		t.Helper()
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release agent archive control transaction: %v", err)
		}
		if err := integrationdb.Await(t, archiveDone, "agent archive"); err != nil {
			t.Fatalf("archive agent after source lock: %v", err)
		}
	}
}

func runScopeDeletionAfterConcurrentAgentArchive(
	t *testing.T,
	ctx context.Context,
	label string,
	deletionGateQuery string,
	deleteScope func(machineLifecycleLockOrderFixture, *executionstore.ActorParams) error,
) machineLifecycleLockOrderFixture {
	t.Helper()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, label)
	controlTx := integrationdb.BeginTx(t, ctx, fixture.pool)
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent before archive: %v", err)
	}

	actor := scopeDeletionActor(t, fixture)
	archiveDone := integrationdb.RunAsyncError(func() error {
		_, _, archiveErr := fixture.store.Execution().IntegrationArchiveAgentOnce(
			ctx,
			testProjectID,
			fixture.agent.ID,
			actor,
		)
		return archiveErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)

	deleteDone := integrationdb.RunAsyncError(func() error { return deleteScope(fixture, actor) })
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, deletionGateQuery, 1)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release archive control transaction: %v", err)
	}
	if err := integrationdb.Await(t, archiveDone, "agent archival"); err != nil {
		t.Fatalf("archive agent that won the source gate: %v", err)
	}
	if err := integrationdb.Await(t, deleteDone, "scope deletion"); err != nil {
		t.Fatalf("delete scope after concurrent agent archival: %v", err)
	}
	return fixture
}

type machineLifecycleLockOrderFixture struct {
	pool             *pgxpool.Pool
	store            *Store
	now              time.Time
	userID           uuid.UUID
	machinePool      executionstore.MachinePoolRecord
	poolGrant        executionstore.ProjectMachinePoolGrantRecord
	agent            executionstore.AgentRecord
	runtimeLock      executionstore.AgentRuntimeLockRecord
	createToolCallID uuid.UUID
	deleteToolCallID uuid.UUID
}

func newMachineLifecycleLockOrderFixture(
	t *testing.T,
	ctx context.Context,
	label string,
) machineLifecycleLockOrderFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, storage.WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		fmt.Sprintf("machine-lifecycle-%s@example.com", label),
		"Machine Lifecycle "+label,
	)
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Machine Lifecycle "+label,
		"test.provider",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"lock-order"}`)},
		2,
		now.Add(time.Second),
	)
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: "machine-lifecycle-grant-" + label,
		},
	)
	if err != nil {
		t.Fatalf("create project machine-pool grant: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,

		fmt.Sprintf(`
instruction: Exercise pool-machine lifecycle locking.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: %s
    max_machines: 2
    initial_num_machines: 0
tools:
  create_machine:
    type: built_in
  delete_machine:
    type: built_in
`, machinePool.Name),
	)
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID:       testProjectID,
		CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatalf("create lifecycle lock-order agent: %v", err)
	}
	runtimeLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire lifecycle lock-order runtime: %v", err)
	}
	toolCalls := createPoolMachineToolCalls(
		t,
		ctx,
		store,
		agent.ID,
		user.ID,
		config.ID,
		runtimeLock,
		"machine-lifecycle-"+label,
		[]poolMachineToolCallSpec{
			{Label: "create", Name: "create_machine", Input: json.RawMessage(`{}`)},
			{Label: "delete", Name: "delete_machine", Input: json.RawMessage(`{}`)},
		},
	)
	return machineLifecycleLockOrderFixture{
		pool:             pool,
		store:            store,
		now:              now,
		userID:           user.ID,
		machinePool:      machinePool,
		poolGrant:        poolGrant,
		agent:            agent,
		runtimeLock:      runtimeLock,
		createToolCallID: toolCalls["create"],
		deleteToolCallID: toolCalls["delete"],
	}
}

func (f machineLifecycleLockOrderFixture) transaction(toolCallID uuid.UUID) executionstore.ExecuteToolCallInput {
	return executionstore.ExecuteToolCallInput{
		RootAgentID:   f.agent.ID,
		ProjectID:     testProjectID,
		AgentID:       f.agent.ID,
		ToolCallID:    toolCallID,
		RuntimeLockID: f.runtimeLock.ID,
	}
}

func (f machineLifecycleLockOrderFixture) createMachineOnce(
	ctx context.Context,
) (executionstore.CreatePoolMachineResult, error) {
	return executeToolCallOnceForLockOrder[executionstore.CreatePoolMachineResult](
		ctx,
		f.store,
		f.transaction(f.createToolCallID),
		executionstore.CreatePoolMachineForToolCall(
			executionstore.CreatePoolMachineInput{MachinePoolID: f.machinePool.ID},
			acceptedPoolMachineCompletionForTest,
		),
	)
}

func executeToolCallOnceForLockOrder[T any](
	ctx context.Context,
	store *Store,
	input executionstore.ExecuteToolCallInput,
	command executionstore.ToolCallCommand,
) (T, error) {
	executed, err := store.Execution().IntegrationExecuteToolCallOnce(ctx, input, command)
	if err != nil {
		var zero T
		return zero, err
	}
	result, ok := executed.CommandResult.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("tool call command returned %T", executed.CommandResult)
	}
	return result, nil
}

func assertPoolMachineRevokedAfterConcurrentCreate(
	t *testing.T,
	ctx context.Context,
	fixture machineLifecycleLockOrderFixture,
	machineID uuid.UUID,
) {
	t.Helper()
	if _, err := fixture.store.Execution().GetProjectMachinePoolGrant(
		ctx,
		testOrgID,
		testProjectID,
		fixture.poolGrant.ID,
	); !storeerr.IsNotFound(err) {
		t.Fatalf("revoked project machine-pool grant lookup error = %v, want not found", err)
	}
	machine, err := fixture.store.Execution().GetMachine(ctx, testOrgID, machineID)
	if err != nil {
		t.Fatalf("load concurrently created and revoked machine: %v", err)
	}
	if machine.LifecycleState != "deleting" || machine.LifecycleReasonCode != "pool_grant_revoked" {
		t.Fatalf("concurrently created and revoked machine = %+v", machine)
	}
	toolCall, err := fixture.store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.agent.ID,
		fixture.createToolCallID,
	)
	if err != nil {
		t.Fatalf("load concurrently completed create-machine tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted {
		t.Fatalf("create-machine tool call state = %q, want completed", toolCall.State)
	}
}

type stepFaultTx struct {
	pgx.Tx
	armed     *atomic.Bool
	committed bool
	after     func()
}

func (tx stepFaultTx) Commit(ctx context.Context) error {
	if !tx.armed.CompareAndSwap(true, false) {
		return tx.Tx.Commit(ctx)
	}
	if tx.committed {
		if err := tx.Tx.Commit(ctx); err != nil {
			return err
		}
	} else if err := tx.Tx.Rollback(ctx); err != nil {
		return err
	}
	if tx.after != nil {
		tx.after()
	}
	return io.ErrUnexpectedEOF
}

type stepResolutionQuery struct {
	dbsqlc.DBTX
	resolve func(context.Context) error
}

type stepErrorRow struct{ err error }

func (r stepErrorRow) Scan(...any) error { return r.err }

func (q stepResolutionQuery) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "GetCommittedToolResult") || strings.Contains(sql, "name: GetToolCall ") {
		if err := q.resolve(ctx); err != nil {
			return stepErrorRow{err}
		}
	}
	return q.DBTX.QueryRow(ctx, sql, args...)
}

func TestCommandToolStoreLostAcknowledgement(t *testing.T) {
	for _, mode := range []string{"before commit", "resolved", "resolution unavailable", "cleanup canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := newMachineLifecycleLockOrderFixture(t, ctx, "command-ack")
			_, err := f.store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
				ProjectID: testProjectID, AgentID: f.agent.ID, ID: f.deleteToolCallID,
				RuntimeLockID: f.runtimeLock.ID, Outcome: executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
			})
			require.NoError(t, err)
			input := f.transaction(f.createToolCallID)
			input.Authorize = true
			require.NotEqual(t, uuid.Nil, input.RootAgentID)
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT model_output_id FROM tool_calls WHERE id=$1`, input.ToolCallID).Scan(&input.BatchOutputID))
			armed := &atomic.Bool{}
			armed.Store(true)
			executionCtx, cancelExecution := context.WithCancel(ctx)
			defer cancelExecution()
			cleanup, cancelCleanup := context.WithCancel(ctx)
			defer cancelCleanup()
			executionCtx = cleanupcontext.WithLifetime(executionCtx, cleanup)
			f.store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
				return stepFaultTx{Tx: tx, armed: armed, committed: mode != "before commit", after: cancelExecution}
			})
			resolving := make(chan struct{}, 1)
			f.store.Execution().IntegrationWrapQueries(func(db dbsqlc.DBTX) dbsqlc.DBTX {
				return stepResolutionQuery{DBTX: db, resolve: func(ctx context.Context) error {
					resolving <- struct{}{}
					if mode == "resolution unavailable" {
						return io.ErrClosedPipe
					}
					if mode == "cleanup canceled" {
						<-ctx.Done()
						return ctx.Err()
					}
					return ctx.Err()
				}}
			})
			plan := func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.CreatePoolMachineForToolCall(
					executionstore.CreatePoolMachineInput{MachinePoolID: f.machinePool.ID}, acceptedPoolMachineCompletionForTest), nil
			}
			done := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
				return f.store.Execution().ExecuteToolCall(executionCtx, input, plan)
			})
			select {
			case <-resolving:
			case <-time.After(3 * time.Second):
				t.Fatal("store did not resolve uncertain command completion")
			}
			if mode == "cleanup canceled" {
				cancelCleanup()
			}
			outcome := integrationdb.Await(t, done, "command completion resolution")
			if mode == "resolved" {
				require.NoError(t, outcome.Err)
				require.True(t, outcome.Value.Applied)
				require.Equal(t, executionstore.StepFinished, outcome.Value.StepDisposition)
			} else {
				require.ErrorIs(t, outcome.Err, storeerr.ErrCommitOutcomeUnknown)
			}
			if mode == "cleanup canceled" {
				require.ErrorIs(t, outcome.Err, context.Canceled)
			}
			if mode == "before commit" {
				var count int
				require.NoError(t, f.pool.QueryRow(ctx,
					`SELECT count(*) FROM tool_call_results WHERE tool_call_id=$1`, input.ToolCallID).Scan(&count))
				require.Zero(t, count)
				return
			}
			newer, found, err := f.store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
			require.NoError(t, err)
			require.True(t, found)
			replayed, err := f.store.Execution().ExecuteToolCall(ctx, input, plan)
			require.NoError(t, err)
			require.False(t, replayed.Applied)
			require.Equal(t, executionstore.StepFinished, replayed.StepDisposition)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_runtime_locks WHERE id=$1`, newer.RuntimeLock.ID).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT count(*) FROM tool_call_results WHERE tool_call_id=$1`, input.ToolCallID).Scan(&count))
			require.Equal(t, 1, count)
			_, err = f.store.Execution().ExecuteToolCall(ctx, input,
				func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
					return executionstore.CreatePoolMachineForToolCall(
						executionstore.CreatePoolMachineInput{MachinePoolID: f.machinePool.ID},
						func(result executionstore.CreatePoolMachineResult) (executionstore.ToolCallCompletionInput, error) {
							completion, err := acceptedPoolMachineCompletionForTest(result)
							completion.Outcome = executionstore.ToolResultOutcomeFailed
							return completion, err
						}), nil
				})
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
		})
	}
}

type lifecycleBarrierTx struct {
	pgx.Tx
	entered chan struct{}
	proceed chan struct{}
	armed   *atomic.Bool
}

func (tx lifecycleBarrierTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "name: LockAgentInProject ") && tx.armed.CompareAndSwap(true, false) {
		close(tx.entered)
		select {
		case <-tx.proceed:
		case <-ctx.Done():
			return stepErrorRow{ctx.Err()}
		}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func TestCommandToolAndGrantRevocationRespectResourceLockOrder(t *testing.T) {
	f := newMachineLifecycleLockOrderFixture(t, t.Context(), "delete-revoke-production-route")
	machine, err := f.createMachineOnce(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	revoker := storage.NewStore(f.pool)
	entered, proceed := make(chan struct{}), make(chan struct{})
	armed := &atomic.Bool{}
	armed.Store(true)
	revoker.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
		return lifecycleBarrierTx{Tx: tx, entered: entered, proceed: proceed, armed: armed}
	})
	revoked := integrationdb.RunAsync(func() (executionstore.DeleteProjectMachinePoolGrantResult, error) {
		return revoker.Execution().DeleteProjectMachinePoolGrant(ctx, testOrgID, testProjectID, f.poolGrant.ID)
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("grant revocation did not reach its agent-lock boundary")
	}
	var attempts atomic.Int32
	f.store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
		attempts.Add(1)
		return tx
	})
	deleted := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
		return f.store.Execution().ExecuteToolCall(ctx, f.transaction(f.deleteToolCallID),
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.DeletePoolMachineForToolCall(
					executionstore.DeletePoolMachineInput{MachineID: machine.Machine.Machine.ID},
					func(executionstore.PoolMachineRecord) (executionstore.ToolCallCompletionInput, error) {
						return executionstore.ToolCallCompletionInput{Outcome: executionstore.ToolResultOutcomeSucceeded,
							ResultContentParts: json.RawMessage(`[{"type":"text","text":"deleted"}]`)}, nil
					}), nil
			})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.pool, "LockMachineForLifecycle", 1)
	close(proceed)
	require.NoError(t, integrationdb.Await(t, revoked, "grant revocation").Err)
	outcome := integrationdb.Await(t, deleted, "command after revocation")
	require.NotErrorIs(t, outcome.Err, context.DeadlineExceeded)
	require.NotErrorIs(t, outcome.Err, context.Canceled)
	require.EqualValues(t, 1, attempts.Load(), "lock order must not require a deadlock retry")
}

type overlappingReplayTx struct {
	pgx.Tx
	query   string
	armed   *atomic.Bool
	commits *atomic.Int32
	read    chan error
	proceed <-chan struct{}
}

func (tx overlappingReplayTx) Commit(ctx context.Context) error {
	tx.commits.Add(1)
	return tx.Tx.Commit(ctx)
}

func (tx overlappingReplayTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, sql, args...)
	if strings.Contains(sql, "name: "+tx.query+" ") && tx.armed.CompareAndSwap(true, false) {
		return overlappingReplayRow{Row: row, done: ctx.Done(), contextErr: ctx.Err, read: tx.read, proceed: tx.proceed}
	}
	return row
}

type overlappingReplayRow struct {
	pgx.Row
	done       <-chan struct{}
	contextErr func() error
	read       chan error
	proceed    <-chan struct{}
}

func (r overlappingReplayRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	r.read <- err
	select {
	case <-r.proceed:
		return err
	case <-r.done:
		return r.contextErr()
	}
}

func assertOverlappingCommandReplay(
	t *testing.T, store *Store, input executionstore.ExecuteToolCallInput,
	plan executionstore.ToolCallPlan, query string, afterCommit func(),
) executionstore.ExecuteToolCallResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	read, proceed := make(chan error, 1), make(chan struct{})
	armed := &atomic.Bool{}
	commits := &atomic.Int32{}
	armed.Store(true)
	store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
		return overlappingReplayTx{Tx: tx, query: query, armed: armed, commits: commits, read: read, proceed: proceed}
	})
	retry := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
		return store.Execution().ExecuteToolCall(ctx, input, plan)
	})
	select {
	case err := <-read:
		if query == "GetToolCall" {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, pgx.ErrNoRows)
		}
	case <-ctx.Done():
		t.Fatal("retry did not read the incomplete command")
	}
	original, err := store.Execution().ExecuteToolCall(ctx, input, plan)
	require.NoError(t, err)
	if afterCommit != nil {
		afterCommit()
	}
	call, err := store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, input.ToolCallID)
	require.NoError(t, err)
	var leaseExists bool
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM agent_runtime_locks WHERE id=$1)`, input.RuntimeLockID).Scan(&leaseExists))
	require.False(t, leaseExists)
	newer, err := store.Execution().AcquireAgentRuntimeLock(ctx, input.ProjectID, input.AgentID,
		testWorkerProcessID, testAgentRuntimeLockLeaseDuration)
	require.NoError(t, err)
	beforeReplay := commits.Load()
	close(proceed)
	result := integrationdb.Await(t, retry, "overlapping command replay")
	require.NoError(t, result.Err)
	require.False(t, result.Value.Applied)
	require.Equal(t, beforeReplay, commits.Load())
	expectedResults := 1
	if call.State == executionstore.ToolCallStateWaiting {
		expectedResults = 0
		require.Equal(t, executionstore.StepTransferred, original.StepDisposition)
		require.Equal(t, executionstore.StepTransferred, result.Value.StepDisposition)
		require.Equal(t, executionstore.ToolCallDispositionWaiting, result.Value.Disposition)
		require.Nil(t, result.Value.Completed)
		require.Equal(t, original.CommandResult, result.Value.CommandResult)
	} else {
		require.Equal(t, executionstore.ToolCallDispositionCompleted, result.Value.Disposition)
		require.NotNil(t, result.Value.Completed)
		if original.Completed != nil {
			require.Equal(t, original.Completed.ID, result.Value.Completed.ID)
			require.JSONEq(t, string(original.Completed.ResultContentParts), string(result.Value.Completed.ResultContentParts))
		}
	}
	var results int
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM tool_call_results WHERE tool_call_id=$1`, input.ToolCallID).Scan(&results))
	require.Equal(t, expectedResults, results)
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM agent_runtime_locks WHERE id=$1)`, newer.ID).Scan(&leaseExists))
	require.True(t, leaseExists)
	return result.Value
}

func TestInteractionHandlerOverlappingReplay(t *testing.T) {
	for _, key := range []string{"", "chat"} {
		t.Run("handler_"+key, func(t *testing.T) {
			f := newIntegrationInteractionFixture(t)
			input := f.selectionCall(t)
			input.RootAgentID = input.AgentID
			require.NoError(t, f.store.pool.QueryRow(f.ctx,
				`SELECT model_output_id FROM tool_calls WHERE id=$1`, input.ToolCallID).Scan(&input.BatchOutputID))
			result := assertOverlappingCommandReplay(t, f.store, input, handlerSelectionPlan(key, `{}`), "GetToolCall", nil)
			require.Equal(t, executionstore.StepFinished, result.StepDisposition)
			selection, err := f.store.Execution().GetInteractionSelection(f.ctx, input.ProjectID, input.AgentID)
			require.NoError(t, err)
			require.Equal(t, key, selection.HandlerKey)
		})
	}
}

func TestStopSubagentOverlappingReplay(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(fmt.Sprintf("archive_%t", archive), func(t *testing.T) {
			ctx := t.Context()
			f, parent, child := executionLockOrderAgents(t, true)
			agent, err := f.Store.Execution().GetAgentInProject(ctx, testProjectID, parent)
			require.NoError(t, err)
			runtime, err := f.Store.Execution().AcquireAgentRuntimeLock(ctx, testProjectID, parent,
				testWorkerProcessID, testAgentRuntimeLockLeaseDuration)
			require.NoError(t, err)
			calls := createReadyToolCallsForTest(t, ctx, f.Store, parent, f.UserID, agent.CurrentConfigID,
				runtime, "overlapping-stop", []toolCallSpecForTest{{Label: "stop", Name: "stop_agent", Input: json.RawMessage(`{}`)}})
			input := executionstore.ExecuteToolCallInput{ProjectID: testProjectID, AgentID: parent,
				RootAgentID: parent, RuntimeLockID: runtime.ID, ToolCallID: calls["stop"]}
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT model_output_id FROM tool_calls WHERE id=$1`, input.ToolCallID).Scan(&input.BatchOutputID))
			completion := executionstore.ToolCallCompletionInput{Outcome: executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(`[{"type":"text","text":"stopped"}]`)}
			plan := func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.StopSubagentForToolCall(executionstore.StopSubagentInput{
					TargetAgentID: child, Archive: archive}, completion), nil
			}
			result := assertOverlappingCommandReplay(t, f.Store, input, plan, "GetToolCall", nil)
			require.Equal(t, executionstore.StepFinished, result.StepDisposition)
			completion.Outcome = executionstore.ToolResultOutcomeFailed
			_, err = f.Store.Execution().ExecuteToolCall(ctx, input, plan)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
		})
	}
}

func TestStartProcessOverlappingReplay(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "overlapping-start")
	toolID := createToolCallForProcessTest(t, ctx, f, "overlapping-start", "run_command")
	input := executionstore.ExecuteToolCallInput{ProjectID: testProjectID, AgentID: f.AgentID,
		RootAgentID: f.AgentID, RuntimeLockID: f.Lock.ID, ToolCallID: toolID}
	command := executionstore.CreateProcessInput{AgentMachineBindingID: f.BindingID,
		ExecutionSpec: processcmd.ForShell("cat", "sh", ""), Cwd: "/work"}
	plan := func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
		return executionstore.StartProcessForToolCall(command), nil
	}
	assertOverlappingCommandReplay(t, f.Store, input, plan, "GetProcessByToolCall", func() {
		process, found, err := f.Store.Execution().GetProcessByToolCall(ctx, testProjectID, f.AgentID, toolID)
		require.NoError(t, err)
		require.True(t, found)
		_, found, err = acceptDaemonProcessForTest(ctx, f.Store, f.OrgID, f.MachineID, f.RuntimeID, process.ID)
		require.NoError(t, err)
		require.True(t, found)
		application, err := f.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
			ProjectID: testProjectID, AgentID: f.AgentID, ID: process.ID, Authority: f.authority(),
			SourceStartedAt: f.Now.Add(time.Second), Result: json.RawMessage(`{"output":"ready","cursor":0,"next_cursor":5,"truncated":false}`),
		})
		require.NoError(t, err)
		require.True(t, application.ToolResultCommitted)
		require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
	})
	command.ExecutionSpec = processcmd.ForShell("different", "sh", "")
	_, err := f.Store.Execution().ExecuteToolCall(ctx, input, plan)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	var count int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM processes WHERE tool_call_id=$1`, toolID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestProcessActionOverlappingReplay(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprintf("already_stopped_%t", stopped), func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "overlapping-action")
			process := startRunningProcessForReadTest(t, ctx, f, "overlapping-action", json.RawMessage(`{"output":"ready","cursor":0,"next_cursor":5,"truncated":false}`))
			name := "read_process"
			command := executionstore.CreateProcessActionInput{ProcessID: process.ID,
				ActionKind: executionstore.ProcessActionKindRead, Payload: json.RawMessage(`{}`)}
			completion := executionstore.ToolCallCompletionInput{Outcome: executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(`[{"type":"text","text":"already stopped"}]`)}
			if stopped {
				name = "stop_process"
				command.ActionKind = executionstore.ProcessActionKindTerminate
				_, err := f.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
					ProjectID: testProjectID, AgentID: f.AgentID, ID: process.ID, Authority: f.authority(),
					State: executionstore.ProcessStateExited, ExitCode: new(0),
					SourceEndedAt: f.Now.Add(3 * time.Second), Result: json.RawMessage(`{}`),
				})
				require.NoError(t, err)
			}
			toolID := createToolCallForProcessTest(t, ctx, f, "overlapping-action", name)
			input := executionstore.ExecuteToolCallInput{ProjectID: testProjectID, AgentID: f.AgentID,
				RootAgentID: f.AgentID, RuntimeLockID: f.Lock.ID, ToolCallID: toolID}
			plan := func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				if stopped {
					return executionstore.StopProcessForToolCall(command, completion), nil
				}
				return executionstore.CreateProcessActionForToolCall(command), nil
			}
			assertOverlappingCommandReplay(t, f.Store, input, plan, "GetProcessActionByToolCall", func() {
				if !stopped {
					action, found, err := f.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, f.AgentID, toolID)
					require.NoError(t, err)
					require.True(t, found)
					_, found, err = f.Store.Execution().AcceptDaemonProcessAction(ctx, executionstore.AcceptDaemonProcessActionInput{
						Authority: f.authority(), ProcessID: process.ID, ID: action.ID,
					})
					require.NoError(t, err)
					require.True(t, found)
					applied, err := f.Store.Execution().ApplyDaemonProcessAction(ctx, executionstore.CompleteDaemonProcessActionInput{
						ProjectID: testProjectID, AgentID: f.AgentID, ProcessID: process.ID, ID: action.ID,
						Authority: f.authority(), Result: json.RawMessage(fmt.Sprintf(
							`{"process_id":%q,"output":"result","cursor":5,"next_cursor":11,"truncated":false}`,
							publicResourceID(publicid.KindProcess, process.ID))),
					})
					require.NoError(t, err)
					require.True(t, applied.ToolResultCommitted)
				}
				require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
			})
			if stopped {
				completion.Outcome = executionstore.ToolResultOutcomeFailed
			} else {
				command.Payload = json.RawMessage(`{"cursor":20}`)
			}
			_, err := f.Store.Execution().ExecuteToolCall(ctx, input, plan)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
		})
	}
}

func processTransferCommandForTest(t *testing.T, kind string) (
	processDaemonFixture, executionstore.ExecuteToolCallInput,
	executionstore.ToolCallPlan, executionstore.ToolCallPlan, string,
) {
	t.Helper()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "transfer-replay")
	query := "GetProcessByToolCall"
	name := "run_command"
	command := executionstore.StartProcessForToolCall(executionstore.CreateProcessInput{
		AgentMachineBindingID: f.BindingID, ExecutionSpec: processcmd.ForShell("cat", "sh", ""), Cwd: "/work",
	})
	conflict := executionstore.StartProcessForToolCall(executionstore.CreateProcessInput{
		AgentMachineBindingID: f.BindingID, ExecutionSpec: processcmd.ForShell("different", "sh", ""), Cwd: "/work",
	})
	if kind != "start" {
		process := startRunningProcessForReadTest(t, ctx, f, "transfer-replay",
			json.RawMessage(`{"output":"ready","cursor":0,"next_cursor":5,"truncated":false}`))
		query = "GetProcessActionByToolCall"
		name = "read_process"
		input := executionstore.CreateProcessActionInput{ProcessID: process.ID,
			ActionKind: executionstore.ProcessActionKindRead, Payload: json.RawMessage(`{}`)}
		if kind == "terminate" {
			name = "stop_process"
			input.ActionKind = executionstore.ProcessActionKindTerminate
		}
		build := func(input executionstore.CreateProcessActionInput) executionstore.ToolCallCommand {
			if kind == "terminate" {
				return executionstore.StopProcessForToolCall(input, executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"stopped"}]`),
				})
			}
			return executionstore.CreateProcessActionForToolCall(input)
		}
		command = build(input)
		input.Payload = json.RawMessage(`{"cursor":20}`)
		conflict = build(input)
	}
	toolID := createToolCallForProcessTest(t, ctx, f, "transfer-replay", name)
	input := executionstore.ExecuteToolCallInput{ProjectID: testProjectID, AgentID: f.AgentID,
		RootAgentID: f.AgentID, RuntimeLockID: f.Lock.ID, ToolCallID: toolID}
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT model_output_id FROM tool_calls WHERE id=$1`, toolID).Scan(&input.BatchOutputID))
	plan := func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) { return command, nil }
	conflicting := func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) { return conflict, nil }
	return f, input, plan, conflicting, query
}

func TestProcessOwnershipTransferLostAcknowledgement(t *testing.T) {
	modes := []string{"before commit", "different owner", "resolved", "resolution unavailable", "cleanup canceled"}
	for _, kind := range []string{"start", "read", "terminate"} {
		for _, mode := range modes {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				ctx := t.Context()
				f, input, plan, conflicting, _ := processTransferCommandForTest(t, kind)
				armed := &atomic.Bool{}
				armed.Store(true)
				executionCtx, cancelExecution := context.WithCancel(ctx)
				defer cancelExecution()
				cleanup, cancelCleanup := context.WithCancel(ctx)
				defer cancelCleanup()
				executionCtx = cleanupcontext.WithLifetime(executionCtx, cleanup)
				var newer uuid.UUID
				var afterErr error
				f.Store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
					return stepFaultTx{Tx: tx, armed: armed,
						committed: mode != "before commit" && mode != "different owner", after: func() {
							cancelExecution()
							if mode == "different owner" {
								_, afterErr = f.Store.Execution().ExecuteToolCall(ctx, input, plan)
								if afterErr != nil {
									return
								}
							}
							if mode != "before commit" {
								lock, err := f.Store.Execution().AcquireAgentRuntimeLock(ctx, input.ProjectID, input.AgentID,
									testWorkerProcessID, testAgentRuntimeLockLeaseDuration)
								afterErr = err
								newer = lock.ID
							}
						}}
				})
				resolving := make(chan struct{}, 1)
				f.Store.Execution().IntegrationWrapQueries(func(db dbsqlc.DBTX) dbsqlc.DBTX {
					return stepResolutionQuery{DBTX: db, resolve: func(ctx context.Context) error {
						resolving <- struct{}{}
						if mode == "resolution unavailable" {
							return io.ErrClosedPipe
						}
						if mode == "cleanup canceled" {
							<-ctx.Done()
						}
						return ctx.Err()
					}}
				})
				done := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
					return f.Store.Execution().ExecuteToolCall(executionCtx, input, plan)
				})
				select {
				case <-resolving:
				case <-time.After(3 * time.Second):
					t.Fatal("store did not resolve uncertain ownership transfer")
				}
				if mode == "cleanup canceled" {
					cancelCleanup()
				}
				outcome := integrationdb.Await(t, done, "ownership transfer resolution")
				require.NoError(t, afterErr)
				f.Store.Execution().IntegrationWrapQueries(func(db dbsqlc.DBTX) dbsqlc.DBTX { return db })
				if mode == "resolved" {
					require.NoError(t, outcome.Err)
					require.True(t, outcome.Value.Applied)
					require.Equal(t, executionstore.StepTransferred, outcome.Value.StepDisposition)
					require.Equal(t, executionstore.ToolCallDispositionWaiting, outcome.Value.Disposition)
					require.Nil(t, outcome.Value.Completed)
				} else {
					require.ErrorIs(t, outcome.Err, storeerr.ErrCommitOutcomeUnknown)
				}
				if mode == "cleanup canceled" {
					require.ErrorIs(t, outcome.Err, context.Canceled)
				}
				call, err := f.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, input.ToolCallID)
				require.NoError(t, err)
				var owners, results int
				require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM processes WHERE tool_call_id=$1) + (SELECT count(*) FROM process_actions WHERE tool_call_id=$1),
 (SELECT count(*) FROM tool_call_results WHERE tool_call_id=$1)`, input.ToolCallID).Scan(&owners, &results))
				require.Zero(t, results)
				if mode == "before commit" {
					require.Zero(t, owners)
					require.Equal(t, executionstore.ToolCallStateReady, call.State)
					return
				}
				require.Equal(t, 1, owners)
				require.Equal(t, executionstore.ToolCallStateWaiting, call.State)
				replayed, err := f.Store.Execution().ExecuteToolCall(ctx, input, plan)
				require.NoError(t, err)
				require.False(t, replayed.Applied)
				require.Equal(t, executionstore.StepTransferred, replayed.StepDisposition)
				require.Equal(t, executionstore.ToolCallDispositionWaiting, replayed.Disposition)
				if mode == "resolved" {
					require.Equal(t, outcome.Value.CommandResult, replayed.CommandResult)
				}
				_, err = f.Store.Execution().ExecuteToolCall(ctx, input, conflicting)
				require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
				var oldExists, newerExists bool
				require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM agent_runtime_locks WHERE id=$1),
 EXISTS(SELECT 1 FROM agent_runtime_locks WHERE id=$2)`, input.RuntimeLockID, newer).Scan(&oldExists, &newerExists))
				require.False(t, oldExists)
				require.True(t, newerExists)
			})
		}
	}
}

func TestProcessOwnershipTransferOverlappingReplay(t *testing.T) {
	for _, kind := range []string{"start", "read", "terminate"} {
		t.Run(kind, func(t *testing.T) {
			f, input, plan, conflicting, query := processTransferCommandForTest(t, kind)
			assertOverlappingCommandReplay(t, f.Store, input, plan, query, nil)
			_, err := f.Store.Execution().ExecuteToolCall(t.Context(), input, conflicting)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
		})
	}
}

func TestProcessOverlappingReplayAfterMachineDeletion(t *testing.T) {
	for _, kind := range []string{"start", "read", "terminate"} {
		t.Run(kind, func(t *testing.T) {
			f, input, plan, conflicting, query := processTransferCommandForTest(t, kind)
			result := assertOverlappingCommandReplay(t, f.Store, input, plan, query, func() {
				_, err := f.Store.Execution().DeleteMachine(t.Context(), executionstore.DeleteMachineInput{
					OrgID: f.OrgID, MachineID: f.MachineID,
				})
				require.NoError(t, err)
			})
			require.Equal(t, executionstore.StepFinished, result.StepDisposition)
			_, err := f.Store.Execution().ExecuteToolCall(t.Context(), input, conflicting)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
		})
	}
}

func TestInteractionHandlerOverlappingReplayAfterRevocation(t *testing.T) {
	f := newIntegrationInteractionFixture(t)
	input := f.selectionCall(t)
	input.RootAgentID = input.AgentID
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT model_output_id FROM tool_calls WHERE id=$1`, input.ToolCallID).Scan(&input.BatchOutputID))
	result := assertOverlappingCommandReplay(t, f.store, input, handlerSelectionPlan("chat", `{}`), "GetToolCall", func() {
		changed, err := f.store.Integrations().DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
			ProjectID: input.ProjectID, IntegrationID: f.integration.ID,
		})
		require.NoError(t, err)
		require.True(t, changed)
	})
	require.Equal(t, executionstore.StepFinished, result.StepDisposition)
}

func TestToolCompletionCoordinatesOnlyFinishedBatch(t *testing.T) {
	for _, last := range []bool{false, true} {
		t.Run(fmt.Sprintf("last_%t", last), func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "completion-window")
			ids := createToolCallBatchForProcessTest(t, ctx, f, "completion-window", []processToolCallBatchItem{
				builtInProcessToolCallBatchItem("first", "list_processes"),
				builtInProcessToolCallBatchItem("last", "list_processes"),
			})
			input := executionstore.ExecuteToolCallInput{ProjectID: testProjectID, AgentID: f.AgentID,
				RootAgentID: f.AgentID, RuntimeLockID: f.Lock.ID, ToolCallID: ids[0]}
			plan := func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.CompleteToolCallInExecution(executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
				}), nil
			}
			if last {
				_, err := f.Store.Execution().ExecuteToolCall(ctx, input, plan)
				require.NoError(t, err)
				input.ToolCallID = ids[1]
			}
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT model_output_id FROM tool_calls WHERE id=$1`, input.ToolCallID).Scan(&input.BatchOutputID))
			input.Completion = &executionstore.StepCompletion{}
			armed := &atomic.Bool{}
			armed.Store(true)
			var canceled atomic.Bool
			f.Store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
				return stepFaultTx{Tx: tx, armed: armed, committed: true, after: func() {
					input.Completion.LeaseLost(func() { canceled.Store(true) })
					require.Equal(t, !last, canceled.Load(), "only a transaction finishing the claim may defer cancellation")
				}}
			})
			result, err := f.Store.Execution().ExecuteToolCall(ctx, input, plan)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Equal(t, last, result.StepDisposition == executionstore.StepFinished)
			input.Completion.LeaseLost(func() { canceled.Store(true) })
			require.Equal(t, !last, canceled.Load())
		})
	}
}
