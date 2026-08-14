//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestPoolMachineCreationAndGrantRevocationSerializePoolBeforeAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "create-revoke")

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lifecycle lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: testOrgID, ID: fixture.machinePool.ID},
	); err != nil {
		t.Fatalf("lock machine pool for lifecycle order: %v", err)
	}

	type createOutcome struct {
		result executionstore.CreatePoolMachineResult
		err    error
	}
	createDone := make(chan createOutcome, 1)
	go func() {
		result, createErr := executeToolCallOnceForLockOrder[executionstore.CreatePoolMachineResult](
			ctx,
			fixture.store,
			fixture.transaction(fixture.createToolCallID),
			executionstore.CreatePoolMachineForToolCall(
				executionstore.CreatePoolMachineInput{MachinePoolID: fixture.machinePool.ID},
				acceptedPoolMachineCompletionForTest,
			),
		)
		createDone <- createOutcome{result: result, err: createErr}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 1)

	agentLockCtx, cancelAgentLock := context.WithTimeout(ctx, 2*time.Second)
	defer cancelAgentLock()
	if _, err := controlQ.LockAgentInProject(
		agentLockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent while pool-machine creation waits for pool: %v", err)
	}

	type revokeOutcome struct {
		result executionstore.DeleteProjectMachinePoolGrantResult
		err    error
	}
	revokeDone := make(chan revokeOutcome, 1)
	go func() {
		result, revokeErr := fixture.store.Execution().DeleteProjectMachinePoolGrant(
			ctx,
			testOrgID,
			testProjectID,
			fixture.poolGrant.ID,
		)
		revokeDone <- revokeOutcome{result: result, err: revokeErr}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForUpdate", 1)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release lifecycle lock control transaction: %v", err)
	}

	var created executionstore.CreatePoolMachineResult
	select {
	case outcome := <-createDone:
		if outcome.err != nil {
			t.Fatalf("create pool machine in one transaction attempt: %v", outcome.err)
		}
		created = outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pool-machine creation")
	}
	var revoked executionstore.DeleteProjectMachinePoolGrantResult
	select {
	case outcome := <-revokeDone:
		if outcome.err != nil {
			t.Fatalf("revoke project machine-pool grant: %v", outcome.err)
		}
		revoked = outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for project machine-pool grant revocation")
	}

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
				fixture.now.Add(7*time.Second),
			)

			controlTx, err := fixture.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin multi-pool control transaction: %v", err)
			}
			defer func() { _ = controlTx.Rollback(ctx) }()
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

			type launchOutcome struct {
				result executionstore.LaunchAgentResult
				err    error
			}
			launchDone := make(chan launchOutcome, 1)
			go func() {
				result, launchErr := fixture.store.Execution().IntegrationLaunchAgentOnce(
					context.Background(),
					executionstore.LaunchAgentInput{
						ProjectID:      testProjectID,
						ProfileID:      profile.ID,
						AgentConfigID:  profile.CurrentConfigID,
						LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
						IdempotencyKey: "multi-pool-launch-" + slug,
					},
				)
				launchDone <- launchOutcome{result: result, err: launchErr}
			}()
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

			select {
			case outcome := <-launchDone:
				if outcome.err != nil {
					t.Fatalf("launch multi-pool agent: %v", outcome.err)
				}
				if len(outcome.result.MachineBindings) != 0 {
					t.Fatalf("multi-pool zero-initial launch bindings = %+v", outcome.result.MachineBindings)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for multi-pool launch")
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

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin machine lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: created.Machine.Machine.ID},
	); err != nil {
		t.Fatalf("lock machine for lifecycle order: %v", err)
	}

	type deleteOutcome struct {
		result executionstore.PoolMachineRecord
		err    error
	}
	deleteDone := make(chan deleteOutcome, 1)
	go func() {
		result, deleteErr := executeToolCallOnceForLockOrder[executionstore.PoolMachineRecord](
			ctx,
			fixture.store,
			fixture.transaction(fixture.deleteToolCallID),
			executionstore.DeletePoolMachineForToolCall(
				executionstore.DeletePoolMachineInput{MachineRef: created.Machine.Binding.MachineRef},
				acceptedPoolMachineCompletionForTest,
			),
		)
		deleteDone <- deleteOutcome{result: result, err: deleteErr}
	}()
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

	select {
	case outcome := <-deleteDone:
		if outcome.err != nil {
			t.Fatalf("delete pool machine in one transaction attempt: %v", outcome.err)
		}
		if outcome.result.Machine.LifecycleState != "deleting" ||
			outcome.result.Machine.LifecycleReasonCode != "machine_tool_delete" ||
			outcome.result.Binding.DeleteToolCallID != fixture.deleteToolCallID {
			t.Fatalf("deleted pool machine = %+v", outcome.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pool-machine tool deletion")
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
			executionstore.DeletePoolMachineInput{MachineRef: created.Machine.Binding.MachineRef},
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

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin pool lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachinePoolForLifecycle(
		ctx,
		dbsqlc.LockMachinePoolForLifecycleParams{OrgID: testOrgID, ID: fixture.machinePool.ID},
	); err != nil {
		t.Fatalf("lock pool before deletion completion: %v", err)
	}

	completeDone := make(chan error, 1)
	go func() {
		completeDone <- fixture.store.Execution().CompletePoolMachineDeletion(
			ctx,
			testOrgID,
			deleted.Machine.ID,
			deleting.Machine.DeleteAttempts,
		)
	}()
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
	select {
	case err := <-completeDone:
		if err != nil {
			t.Fatalf("complete pool machine deletion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pool-machine deletion completion")
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

			controlTx, err := fixture.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin machine lock control transaction: %v", err)
			}
			defer func() { _ = controlTx.Rollback(ctx) }()
			if _, err := dbsqlc.New(controlTx).LockMachineForLifecycle(
				ctx,
				dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: machine.ID},
			); err != nil {
				t.Fatalf("lock machine for daemon token contention: %v", err)
			}

			tokenDone := make(chan error, 1)
			deleteDone := make(chan error, 1)
			startToken := func() {
				go func() {
					_, tokenErr := fixture.store.Execution().CreateBYOMachineDaemonToken(
						context.Background(),
						executionstore.CreateBYOMachineDaemonTokenInput{
							OrgID:     testOrgID,
							MachineID: machine.ID,
							Name:      "contention token",
						},
					)
					tokenDone <- tokenErr
				}()
			}
			startDelete := func() {
				go func() {
					_, deleteErr := fixture.store.Execution().DeleteMachine(
						context.Background(),
						executionstore.DeleteMachineInput{OrgID: testOrgID, MachineID: machine.ID},
					)
					deleteDone <- deleteErr
				}()
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

			tokenErr := <-tokenDone
			if tokenWins && tokenErr != nil {
				t.Fatalf("create daemon token before deletion: %v", tokenErr)
			}
			if !tokenWins && !errors.Is(tokenErr, storeerr.ErrNotFound) {
				t.Fatalf("create daemon token after deletion error = %v, want not found", tokenErr)
			}
			if err := <-deleteDone; err != nil {
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
			MachineRef:            "mchr-prjdel",
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

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin explicit machine lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: machine.ID},
	); err != nil {
		t.Fatalf("lock explicit machine for lifecycle order: %v", err)
	}

	projectDeleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
			ctx,
			testOrgID,
			testProjectID,
			actor,
		)
		projectDeleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)

	machineDeleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := fixture.store.Execution().IntegrationDeleteMachineOnce(
			ctx,
			executionstore.DeleteMachineInput{OrgID: testOrgID, MachineID: machine.ID},
		)
		machineDeleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release explicit machine lock control transaction: %v", err)
	}
	for label, done := range map[string]<-chan error{
		"project deletion": projectDeleteDone,
		"machine deletion": machineDeleteDone,
	} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s after machine lock release: %v", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
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
		time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
	)

	controlTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin project lifecycle control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if err := controlQ.LockProjectLifecycleExclusive(
		ctx,
		dbsqlc.LockProjectLifecycleExclusiveParams{ProjectID: testProjectID},
	); err != nil {
		t.Fatalf("lock project lifecycle exclusively: %v", err)
	}

	type launchOutcome struct {
		result executionstore.LaunchAgentResult
		err    error
	}
	launchDone := make(chan launchOutcome, 1)
	go func() {
		result, launchErr := store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID},
			IdempotencyKey: "project-profile-order",
		})
		launchDone <- launchOutcome{result: result, err: launchErr}
	}()
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

	select {
	case outcome := <-launchDone:
		if outcome.err != nil {
			t.Fatalf("launch after project lifecycle release: %v", outcome.err)
		}
		if !outcome.result.Created {
			t.Fatalf("launch result = %+v, want a new agent", outcome.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent launch")
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
		fixture.now.Add(6*time.Second),
	)
	nextConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		fixture.store,
		"shared-environment-change",
		sourceYAML,
		fixture.now.Add(7*time.Second),
	)

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin pool and environment lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if _, err := controlQ.LockMachinePoolForUpdate(
		ctx,
		dbsqlc.LockMachinePoolForUpdateParams{OrgID: testOrgID, ID: fixture.machinePool.ID},
	); err != nil {
		t.Fatalf("lock shared machine pool: %v", err)
	}

	type launchOutcome struct {
		result executionstore.LaunchAgentResult
		err    error
	}
	launchDone := make(chan launchOutcome, 1)
	go func() {
		result, launchErr := fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
			IdempotencyKey: "shared-environment-launch",
		})
		launchDone <- launchOutcome{result: result, err: launchErr}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 1)

	environmentCtx, cancelEnvironment := context.WithTimeout(ctx, 2*time.Second)
	defer cancelEnvironment()
	if err := controlQ.LockMachineEnvironmentKey(
		environmentCtx,
		dbsqlc.LockMachineEnvironmentKeyParams{MachineID: machine.ID},
	); err != nil {
		t.Fatalf("lock machine environment while launch waits for pool: %v", err)
	}

	type configChangeOutcome struct {
		result executionstore.ChangeAgentConfigResult
		err    error
	}
	configDone := make(chan configChangeOutcome, 1)
	go func() {
		result, changeErr := fixture.store.Execution().IntegrationChangeAgentConfigOnce(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: changeInputFromRecord(nextConfig),
			AgentID:                fixture.agent.ID,
			ActorType:              identitystore.PrincipalTypeUser,
			ActorID:                fixture.userID,
			IdempotencyKey:         "shared-environment-change",
		})
		configDone <- configChangeOutcome{result: result, err: changeErr}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachinePoolForLifecycle", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release pool and environment lock control transaction: %v", err)
	}
	var launched executionstore.LaunchAgentResult
	select {
	case outcome := <-launchDone:
		if outcome.err != nil {
			t.Fatalf("launch after pool lock release: %v", outcome.err)
		}
		launched = outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for launch")
	}
	var changed executionstore.ChangeAgentConfigResult
	select {
	case outcome := <-configDone:
		if outcome.err != nil {
			t.Fatalf("configuration reconciliation after pool lock release: %v", outcome.err)
		}
		changed = outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for configuration reconciliation")
	}

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
			fixture.now.Add(6*time.Second),
		)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin explicit launch control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockMachineEnvironmentKey(
			ctx,
			dbsqlc.LockMachineEnvironmentKeyParams{MachineID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine environment: %v", err)
		}

		launchDone := make(chan launchAttemptOutcome, 1)
		go func() {
			result, launchErr := fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
				ProjectID:      testProjectID,
				ProfileID:      profile.ID,
				AgentConfigID:  profile.CurrentConfigID,
				LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
				IdempotencyKey: "explicit-launch-wins",
			})
			launchDone <- launchAttemptOutcome{result: result, err: launchErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineEnvironmentKey", 1)

		revokeDone := make(chan grantRevocationAttemptOutcome, 1)
		go func() {
			result, revokeErr := fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
			revokeDone <- grantRevocationAttemptOutcome{result: result, err: revokeErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit launch control transaction: %v", err)
		}

		launched := receiveLaunchAttempt(t, launchDone)
		if !launched.Created || len(launched.MachineBindings) != 1 ||
			launched.MachineBindings[0].MachineID != explicit.machine.ID {
			t.Fatalf("launch result = %+v", launched)
		}
		revoked := receiveGrantRevocationAttempt(t, revokeDone)
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
			fixture.now.Add(6*time.Second),
		)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin explicit revocation control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if _, err := dbsqlc.New(controlTx).LockMachineForLifecycle(
			ctx,
			dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine: %v", err)
		}

		revokeDone := make(chan grantRevocationAttemptOutcome, 1)
		go func() {
			result, revokeErr := fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
			revokeDone <- grantRevocationAttemptOutcome{result: result, err: revokeErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)

		launchDone := make(chan launchAttemptOutcome, 1)
		go func() {
			result, launchErr := fixture.store.Execution().IntegrationLaunchAgentOnce(ctx, executionstore.LaunchAgentInput{
				ProjectID:      testProjectID,
				ProfileID:      profile.ID,
				AgentConfigID:  profile.CurrentConfigID,
				LaunchedBy:     identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
				IdempotencyKey: "explicit-revoke-launch",
			})
			launchDone <- launchAttemptOutcome{result: result, err: launchErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 2)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit revocation control transaction: %v", err)
		}

		receiveGrantRevocationAttempt(t, revokeDone)
		select {
		case outcome := <-launchDone:
			if !errors.Is(outcome.err, storeerr.ErrNotFound) {
				t.Fatalf("launch after revocation error = %v, want not found", outcome.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for rejected launch")
		}
		assertExplicitGrantLifecycleOutcome(t, ctx, fixture, explicit, NilID, 0, 0)
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
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "explicit-config-wins")
		explicit := createExplicitGrantLifecycleFixture(t, ctx, fixture, "explicit-config-wins")
		nextConfig := mustCreateAgentConfigFromYAML(
			t,
			ctx,
			fixture.store,
			"explicit-config-wins",
			explicit.configYAML,
			fixture.now.Add(6*time.Second),
		)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin explicit config control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockMachineEnvironmentKey(
			ctx,
			dbsqlc.LockMachineEnvironmentKeyParams{MachineID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine environment: %v", err)
		}

		configDone := make(chan configChangeAttemptOutcome, 1)
		go func() {
			result, changeErr := fixture.store.Execution().IntegrationChangeAgentConfigOnce(
				ctx,
				executionstore.ChangeAgentConfigInput{
					CreateAgentConfigInput: changeInputFromRecord(nextConfig),
					AgentID:                fixture.agent.ID,
					ActorType:              identitystore.PrincipalTypeUser,
					ActorID:                fixture.userID,
					IdempotencyKey:         "explicit-config-wins",
				},
			)
			configDone <- configChangeAttemptOutcome{result: result, err: changeErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineEnvironmentKey", 1)

		revokeDone := make(chan grantRevocationAttemptOutcome, 1)
		go func() {
			result, revokeErr := fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
			revokeDone <- grantRevocationAttemptOutcome{result: result, err: revokeErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit config control transaction: %v", err)
		}

		changed := receiveConfigChangeAttempt(t, configDone)
		if changed.AgentConfig.ID != nextConfig.ID {
			t.Fatalf("activated config = %s, want %s", changed.AgentConfig.ID, nextConfig.ID)
		}
		receiveGrantRevocationAttempt(t, revokeDone)
		assertExplicitGrantLifecycleOutcome(t, ctx, fixture, explicit, fixture.agent.ID, 0, 1)
		assertAgentCurrentConfig(t, ctx, fixture, fixture.agent.ID, nextConfig.ID)
	})

	t.Run("revocation wins", func(t *testing.T) {
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "explicit-revoke-config")
		explicit := createExplicitGrantLifecycleFixture(t, ctx, fixture, "explicit-revoke-config")
		nextConfig := mustCreateAgentConfigFromYAML(
			t,
			ctx,
			fixture.store,
			"explicit-revoke-config",
			explicit.configYAML,
			fixture.now.Add(6*time.Second),
		)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin explicit config revocation control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if _, err := dbsqlc.New(controlTx).LockMachineForLifecycle(
			ctx,
			dbsqlc.LockMachineForLifecycleParams{OrgID: testOrgID, ID: explicit.machine.ID},
		); err != nil {
			t.Fatalf("lock explicit machine: %v", err)
		}

		revokeDone := make(chan grantRevocationAttemptOutcome, 1)
		go func() {
			result, revokeErr := fixture.store.Execution().IntegrationDeleteProjectMachineGrantOnce(
				ctx,
				testOrgID,
				testProjectID,
				explicit.grant.ID,
			)
			revokeDone <- grantRevocationAttemptOutcome{result: result, err: revokeErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 1)

		configDone := make(chan configChangeAttemptOutcome, 1)
		go func() {
			result, changeErr := fixture.store.Execution().IntegrationChangeAgentConfigOnce(
				ctx,
				executionstore.ChangeAgentConfigInput{
					CreateAgentConfigInput: changeInputFromRecord(nextConfig),
					AgentID:                fixture.agent.ID,
					ActorType:              identitystore.PrincipalTypeUser,
					ActorID:                fixture.userID,
					IdempotencyKey:         "explicit-revoke-config",
				},
			)
			configDone <- configChangeAttemptOutcome{result: result, err: changeErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockMachineForLifecycle", 2)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release explicit config revocation control transaction: %v", err)
		}

		receiveGrantRevocationAttempt(t, revokeDone)
		select {
		case outcome := <-configDone:
			if !errors.Is(outcome.err, storeerr.ErrNotFound) {
				t.Fatalf("config change after revocation error = %v, want not found", outcome.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for rejected config change")
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

type launchAttemptOutcome struct {
	result executionstore.LaunchAgentResult
	err    error
}

type configChangeAttemptOutcome struct {
	result executionstore.ChangeAgentConfigResult
	err    error
}

type grantRevocationAttemptOutcome struct {
	result executionstore.ProjectMachineGrantRecord
	err    error
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

func receiveLaunchAttempt(t *testing.T, done <-chan launchAttemptOutcome) executionstore.LaunchAgentResult {
	t.Helper()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("launch in one transaction attempt: %v", outcome.err)
		}
		return outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for launch")
		return executionstore.LaunchAgentResult{}
	}
}

func receiveConfigChangeAttempt(t *testing.T, done <-chan configChangeAttemptOutcome) executionstore.ChangeAgentConfigResult {
	t.Helper()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("config change in one transaction attempt: %v", outcome.err)
		}
		return outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for config change")
		return executionstore.ChangeAgentConfigResult{}
	}
}

func receiveGrantRevocationAttempt(
	t *testing.T,
	done <-chan grantRevocationAttemptOutcome,
) executionstore.ProjectMachineGrantRecord {
	t.Helper()
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("grant revocation in one transaction attempt: %v", outcome.err)
		}
		return outcome.result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for grant revocation")
		return executionstore.ProjectMachineGrantRecord{}
	}
}

func assertExplicitGrantLifecycleOutcome(
	t *testing.T,
	ctx context.Context,
	fixture machineLifecycleLockOrderFixture,
	explicit explicitGrantLifecycleFixture,
	agentID ID,
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
	agentID, wantConfigID ID,
) {
	t.Helper()
	var configID ID
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

func TestProjectDeletionReplansAfterConcurrentAgentArchive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := runScopeDeletionAfterConcurrentAgentArchive(
		t,
		ctx,
		"project-archive",
		func(fixture machineLifecycleLockOrderFixture) error {
			_, err := fixture.store.Organizations().DeleteProject(
				ctx,
				testOrgID,
				testProjectID,
				identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
			)
			return err
		},
	)
	if _, err := fixture.store.Identity().GetProject(ctx, testOrgID, testProjectID); err == nil {
		t.Fatal("project remained active after deletion")
	}
}

func TestOrganizationDeletionReplansAfterConcurrentAgentArchive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := runScopeDeletionAfterConcurrentAgentArchive(
		t,
		ctx,
		"organization-archive",
		func(fixture machineLifecycleLockOrderFixture) error {
			_, err := fixture.store.Organizations().DeleteOrganization(
				ctx,
				testOrgID,
				identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
			)
			return err
		},
	)
	if _, err := fixture.store.Identity().GetOrg(ctx, testOrgID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted organization lookup error = %v, want not found", err)
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

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if err := controlQ.LockAgentMachineSources(
		ctx,
		dbsqlc.LockAgentMachineSourcesParams{AgentID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent machine sources: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := fixture.store.Organizations().DeleteProject(
			ctx,
			testOrgID,
			testProjectID,
			identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
		)
		deleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

	poolGrantDone := make(chan error, 1)
	go func() {
		_, createErr := fixture.store.Execution().CreateProjectMachinePoolGrant(
			ctx,
			executionstore.CreateProjectMachinePoolGrantInput{
				OrgID:          testOrgID,
				ProjectID:      testProjectID,
				MachinePoolID:  targetPool.ID,
				IdempotencyKey: "project-admission-pool-grant",
			},
		)
		poolGrantDone <- createErr
	}()
	explicitGrantDone := make(chan error, 1)
	go func() {
		_, _, createErr := fixture.store.Execution().CreateProjectMachineGrant(
			ctx,
			executionstore.CreateProjectMachineGrantInput{
				OrgID:          testOrgID,
				ProjectID:      testProjectID,
				MachineID:      targetMachine.ID,
				IdempotencyKey: "project-admission-explicit-grant",
			},
		)
		explicitGrantDone <- createErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release source lock control transaction: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete project: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for project deletion")
	}
	for label, done := range map[string]<-chan error{
		"machine-pool grant": poolGrantDone,
		"explicit grant":     explicitGrantDone,
	} {
		select {
		case err := <-done:
			if !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("%s creation after project deletion error = %v, want not found", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s creation", label)
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

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	if err := dbsqlc.New(controlTx).LockAgentMachineSources(
		ctx,
		dbsqlc.LockAgentMachineSourcesParams{AgentID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent machine sources: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := fixture.store.Organizations().DeleteProject(
			ctx,
			testOrgID,
			testProjectID,
			identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
		)
		deleteDone <- deleteErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

	reconcileDone := make(chan error, 1)
	go func() {
		_, reconcileErr := fixture.store.Execution().ReconcileAgentMCPConnections(
			ctx,
			testProjectID,
			fixture.agent.ID,
			[]agentconfig.RuntimeMCPServer{{
				ServerKey: "project-delete-race",
				URL:       "https://example.com/mcp",
			}},
		)
		reconcileDone <- reconcileErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 1)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release source lock control transaction: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete project: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for project deletion")
	}
	select {
	case err := <-reconcileDone:
		if !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("mcp reconciliation after project deletion error = %v, want not found", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mcp reconciliation")
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

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(controlTx).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent before archive: %v", err)
	}

	archiveDone := make(chan error, 1)
	go func() {
		_, _, archiveErr := fixture.store.Execution().ArchiveAgent(
			ctx,
			testProjectID,
			fixture.agent.ID,
			identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
		)
		archiveDone <- archiveErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)

	reconcileDone := make(chan error, 1)
	go func() {
		_, reconcileErr := fixture.store.Execution().ReconcileAgentMCPConnections(
			ctx,
			testProjectID,
			fixture.agent.ID,
			[]agentconfig.RuntimeMCPServer{{
				ServerKey: "archive-race",
				URL:       "https://example.com/mcp",
			}},
		)
		reconcileDone <- reconcileErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release agent lock control transaction: %v", err)
	}
	select {
	case err := <-archiveDone:
		if err != nil {
			t.Fatalf("archive agent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent archival")
	}
	select {
	case err := <-reconcileDone:
		if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
			t.Fatalf("mcp reconciliation after archive error = %v, want state transition conflict", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mcp reconciliation")
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

func runScopeDeletionAfterConcurrentAgentArchive(
	t *testing.T,
	ctx context.Context,
	label string,
	deleteScope func(machineLifecycleLockOrderFixture) error,
) machineLifecycleLockOrderFixture {
	t.Helper()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, label)
	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if err := controlQ.LockAgentMachineSources(
		ctx,
		dbsqlc.LockAgentMachineSourcesParams{AgentID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent machine sources: %v", err)
	}

	archiveDone := make(chan error, 1)
	go func() {
		_, _, archiveErr := fixture.store.Execution().ArchiveAgent(
			ctx,
			testProjectID,
			fixture.agent.ID,
			identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
		)
		archiveDone <- archiveErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- deleteScope(fixture) }()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 2)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release source lock control transaction: %v", err)
	}
	select {
	case err := <-archiveDone:
		if err != nil {
			t.Fatalf("archive agent that won the source gate: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent archival")
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete scope after concurrent agent archival: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for scope deletion")
	}
	return fixture
}

type machineLifecycleLockOrderFixture struct {
	pool             *pgxpool.Pool
	store            *Store
	now              time.Time
	userID           ID
	machinePool      executionstore.MachinePoolRecord
	poolGrant        executionstore.ProjectMachinePoolGrantRecord
	agent            executionstore.AgentRecord
	runtimeLock      executionstore.AgentRuntimeLockRecord
	createToolCallID ID
	deleteToolCallID ID
}

func newMachineLifecycleLockOrderFixture(
	t *testing.T,
	ctx context.Context,
	label string,
) machineLifecycleLockOrderFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
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
		"machine-lifecycle-config-"+label,
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
		now.Add(3*time.Second),
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

func (f machineLifecycleLockOrderFixture) transaction(toolCallID ID) executionstore.ExecuteToolCallInput {
	return executionstore.ExecuteToolCallInput{
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
	machineID ID,
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
