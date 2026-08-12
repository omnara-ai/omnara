//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func liveProcessReconciliationClaimForTest(
	processID ID,
	actions ...executionstore.ProcessActionReconciliationClaim,
) executionstore.ProcessReconciliationClaim {
	return executionstore.ProcessReconciliationClaim{
		ProcessID:            processID,
		SupervisorInstanceID: "test-supervisor-instance",
		Phase:                daemonprotocol.ProcessPhaseAccepted,
		SupervisorLive:       true,
		ExecutionCommitted:   true,
		Actions:              actions,
	}
}

func terminalProcessReconciliationClaimForTest(processID ID) executionstore.ProcessReconciliationClaim {
	return executionstore.ProcessReconciliationClaim{
		ProcessID:             processID,
		SupervisorInstanceID:  "test-supervisor-instance",
		Phase:                 daemonprotocol.ProcessPhaseTerminal,
		ExecutionCommitted:    true,
		ActionAdmissionClosed: true,
	}
}

func processReconciliationDirectiveForTest(
	t *testing.T,
	reconciliation executionstore.DaemonRuntimeReconciliation,
	processID ID,
) executionstore.ProcessReconciliationDirective {
	t.Helper()
	for _, disposition := range reconciliation.Processes {
		if disposition.ProcessID == processID {
			return disposition
		}
	}
	t.Fatalf(
		"no reconciliation directive for process %s in %+v",
		processID,
		reconciliation,
	)
	return executionstore.ProcessReconciliationDirective{}
}

func TestDaemonRuntimeUpdatesMachineLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	mustCreateProjectOperatorUser(t, ctx, store, "runtime-machine@example.com", "Runtime Machine Tester")
	createdMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Runtime Machine",
			IdempotencyKey: "idem-runtime-machine",
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	_, machine, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      createdMachine.ID,
			IdempotencyKey: "idem-pmg-runtime-machine",
		},
	)
	if err != nil {
		t.Fatalf("create project machine grant: %v", err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: createdMachine.ID,
			Name:      "daemon",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	runtimeA, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            createdMachine.OrgID,
			MachineID:        createdMachine.ID,
			DaemonTokenID:    token.Record.ID,
			DaemonInstanceID: testID("daemon-runtime-machine-a"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ObservedPlatform: json.RawMessage(`{"os":"linux","arch":"amd64"}`),
		},
	)
	if err != nil {
		t.Fatalf("register first daemon runtime: %v", err)
	}
	assertMachineState(t, ctx, store, machine.ID, executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOnline)
	assertMachineObservedPlatform(t, ctx, store, machine.ID, "linux", "amd64")
	if _, err := store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority: executionstore.DaemonRuntimeAuthority{
				OrgID:           createdMachine.OrgID,
				MachineID:       createdMachine.ID,
				DaemonRuntimeID: runtimeA.ID,
				DaemonTokenID:   token.Record.ID,
			},
			DaemonInstanceID: testID("daemon-runtime-machine-a"),
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ObservedPlatform: json.RawMessage(`{"os":"darwin","arch":"arm64"}`),
		},
	); err != nil {
		t.Fatalf("heartbeat daemon runtime: %v", err)
	}
	assertMachineObservedPlatform(t, ctx, store, machine.ID, "darwin", "arm64")
	runtimeB, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            createdMachine.OrgID,
			MachineID:        createdMachine.ID,
			DaemonTokenID:    token.Record.ID,
			DaemonInstanceID: testID("daemon-runtime-machine-b"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("register replacement daemon runtime: %v", err)
	}
	assertMachineState(t, ctx, store, machine.ID, executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOnline)
	if _, err := store.Execution().EndDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           createdMachine.OrgID,
			MachineID:       createdMachine.ID,
			DaemonRuntimeID: runtimeA.ID,
			DaemonTokenID:   token.Record.ID,
		},
	); err == nil {
		t.Fatalf("ending superseded runtime should fail lease check")
	} else if !errors.Is(
		err,
		storeerr.ErrDaemonRuntimeUnregistered,
	) {
		t.Fatalf("ending superseded runtime error = %v, want ErrDaemonRuntimeUnregistered", err)
	}
	assertMachineState(t, ctx, store, machine.ID, executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOnline)
	if _, err := store.Execution().EndDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           createdMachine.OrgID,
			MachineID:       createdMachine.ID,
			DaemonRuntimeID: runtimeB.ID,
			DaemonTokenID:   token.Record.ID,
		},
	); err != nil {
		t.Fatalf("end active daemon runtime: %v", err)
	}
	assertMachineState(t, ctx, store, machine.ID, executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOffline)
}

func TestDaemonRuntimeVersionPersistsPerInstance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime-version")

	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "2.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); !errors.Is(err, storeerr.ErrDaemonInstanceSuperseded) {
		t.Fatalf("conflicting daemon version error = %v, want ErrDaemonInstanceSuperseded", err)
	}
	replacement, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "0.0.0-dev",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("register replacement daemon runtime: %v", err)
	}
	if replacement.ID == fixture.RuntimeID || replacement.DaemonVersion != "0.0.0-dev" {
		t.Fatalf("replacement runtime = %+v, want new development runtime", replacement)
	}
	version, registered, err := fixture.Store.Execution().RegisteredDaemonRuntimeVersion(
		ctx,
		fixture.authorityForRuntime(replacement.ID),
	)
	if err != nil {
		t.Fatalf("get replacement daemon runtime version: %v", err)
	}
	if !registered || version != "0.0.0-dev" {
		t.Fatalf("replacement runtime version = %q, %t, want 0.0.0-dev, true", version, registered)
	}
}

func TestRegisterDaemonRuntimeRejectsInvalidVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "invalid-runtime-version")
	_, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.2",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "daemon version") {
		t.Fatalf("register invalid version error = %v", err)
	}
}

func TestLostProcessAcceptAcknowledgementRecoversThroughRegistration(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "lost-process-accept-ack")
	process, err := startProcessForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ToolCallID: createToolCallForProcessTest(
				t,
				ctx,
				fixture,
				"lost-process-accept-ack-start",
				"run_command",
			),
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "sleep 1",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil || !found {
		t.Fatalf("accept process: found=%t err=%v", found, err)
	}

	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{{
				ProcessID:            process.ID,
				SupervisorInstanceID: "prepared-supervisor-instance",
				Phase:                daemonprotocol.ProcessPhasePrepared,
				SupervisorLive:       true,
			}},
		},
	)
	if err != nil {
		t.Fatalf("reconcile lost process acceptance acknowledgement: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionStart {
		t.Fatalf(
			"lost-ack process disposition = %+v, want start",
			disposition,
		)
	}
	offers, err := fixture.Store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authorityForRuntime(registration.Runtime.ID),
			Limit:     1,
		},
	)
	if err != nil {
		t.Fatalf("list offers after lost-ack reconciliation: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf(
			"accepted process returned through normal offers: %+v",
			offers,
		)
	}
}

func TestRegisterDaemonRuntimeLeavesReadyAgentMachineBindingsAttached(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 10, 30, 0, 0, time.UTC)
	_, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "runtime-bindings@example.com", DisplayName: "Runtime Bindings Tester"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Runtime Bindings Machine",
			IdempotencyKey: "idem-runtime-bindings-machine",
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	grant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "idem-runtime-bindings-grant",
		},
	)
	if err != nil {
		t.Fatalf("create project machine grant: %v", err)
	}
	firstAgent := mustCreateAgent(t, ctx, store, now.Add(time.Second))
	secondAgent := mustCreateAgent(t, ctx, store, now.Add(2*time.Second))
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               firstAgent,
			ProjectMachineGrantID: grant.ID,
			MachineRef:            "mchr-actv01",
			BindingKind:           "explicit",
		},
	); err != nil {
		t.Fatalf("bind first attached machine: %v", err)
	}
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               secondAgent,
			ProjectMachineGrantID: grant.ID,
			MachineRef:            "mchr-actv02",
			BindingKind:           "explicit",
		},
	); err != nil {
		t.Fatalf("bind second attached machine: %v", err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: machine.ID,
			Name:      "daemon",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	if _, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.Record.ID,
			DaemonInstanceID: testID("daemon-runtime-bindings"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}
	var attachedCount int
	if err := store.pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_machine_bindings
WHERE project_id = $1 AND machine_id = $2 AND state = 'attached'
	`, testProjectID, machine.ID).
		Scan(&attachedCount); err != nil {
		t.Fatalf("count attached bindings: %v", err)
	}
	if attachedCount != 2 {
		t.Fatalf("attached bindings = %d, want 2", attachedCount)
	}
}

func TestRevokeBYOMachineDaemonTokenEndsRuntimeAndBlocksProcessStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "revoke_token_runtime")

	if _, err := fixture.Store.Execution().RevokeBYOMachineDaemonToken(
		ctx,
		testOrgID,
		fixture.MachineID,
		fixture.TokenID,
		"revoked"); err != nil {
		t.Fatalf("revoke daemon token: %v", err)
	}
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOffline,
	)

	_, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    createToolCallForProcessTest(t, ctx, fixture, "revoke_token_runtime_start", "run_command"),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "bash",
		ShellSelector:         "sh",
	})
	if !errors.Is(err, storeerr.ErrMachineNotReachable) {
		t.Fatalf("start process after daemon token revoke error = %v, want ErrMachineNotReachable", err)
	}
}

func TestDaemonRuntimeCredentialRotationPreservesIdentityAndRevocationOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "revoke_token_runtime_isolation")
	publisher := &recordingPostCommitPublisher{}
	store := newIntegrationStore(fixture.Store.pool, WithPostCommitPublisher(publisher))
	replacementToken, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: fixture.MachineID,
			Name:      "replacement-token",
		},
	)
	if err != nil {
		t.Fatalf("create replacement token: %v", err)
	}

	refreshed, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    replacementToken.Record.ID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("refresh daemon runtime with replacement token: %v", err)
	}
	if refreshed.ID != fixture.RuntimeID {
		t.Fatalf("refreshed runtime id = %s, want %s", refreshed.ID, fixture.RuntimeID)
	}
	if refreshed.DaemonTokenID != replacementToken.Record.ID {
		t.Fatalf(
			"refreshed runtime daemon token id = %s, want %s",
			refreshed.DaemonTokenID,
			replacementToken.Record.ID,
		)
	}

	if _, err := store.Execution().RevokeBYOMachineDaemonToken(
		ctx,
		testOrgID,
		fixture.MachineID,
		fixture.TokenID,
		"old_credential_revoked"); err != nil {
		t.Fatalf("revoke old daemon token: %v", err)
	}
	var activeState executionstore.DaemonRuntimeState
	if err := store.pool.QueryRow(ctx, `
SELECT state
FROM daemon_runtimes
WHERE org_id = $1 AND machine_id = $2 AND id = $3
`, testOrgID, fixture.MachineID, fixture.RuntimeID).
		Scan(&activeState); err != nil {
		t.Fatalf("load runtime state after old token revoke: %v", err)
	}
	if activeState != executionstore.DaemonRuntimeStateActive {
		t.Fatalf("runtime state after old token revoke = %s, want active", activeState)
	}
	if got := publisher.runtimeEndedCount(fixture.RuntimeID); got != 0 {
		t.Fatalf("runtime-ended notifications after old token revoke = %d, want 0", got)
	}

	if _, err := store.Execution().RevokeBYOMachineDaemonToken(
		ctx,
		testOrgID,
		fixture.MachineID,
		replacementToken.Record.ID,
		"current_credential_revoked"); err != nil {
		t.Fatalf("revoke current daemon token: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `
SELECT state
FROM daemon_runtimes
WHERE org_id = $1 AND machine_id = $2 AND id = $3
`, testOrgID, fixture.MachineID, fixture.RuntimeID).
		Scan(&activeState); err != nil {
		t.Fatalf("load runtime state after current token revoke: %v", err)
	}
	if activeState != executionstore.DaemonRuntimeStateEnded {
		t.Fatalf("runtime state after current token revoke = %s, want ended", activeState)
	}
	if got := publisher.runtimeEndedCount(fixture.RuntimeID); got != 1 {
		t.Fatalf("runtime-ended notifications after current token revoke = %d, want 1", got)
	}
}

func TestDaemonRuntimeCredentialTransferRevokesOldRuntimeAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_credential_authority")
	replacementToken, err := fixture.Store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     fixture.OrgID,
			MachineID: fixture.MachineID,
			Name:      "replacement-authority",
		},
	)
	if err != nil {
		t.Fatalf("create replacement daemon token: %v", err)
	}
	replacementAuthority := fixture.authority()
	replacementAuthority.DaemonTokenID = replacementToken.Record.ID

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"runtime_credential_authority_process",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if offers, err := fixture.Store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{Authority: replacementAuthority, Limit: 10},
	); err != nil || len(offers) != 0 {
		t.Fatalf("replacement token offers before transfer = %+v err=%v, want none", offers, err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: replacementAuthority,
		ProcessID: process.ID,
	}); err != nil || found {
		t.Fatalf("replacement token accept before transfer found=%v err=%v, want rejected", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       replacementAuthority,
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		SourceStartedAt: fixture.Now.Add(time.Second),
	}); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("replacement token report before transfer error = %v, want unregistered", err)
	}

	refreshed, err := fixture.Store.Execution().RegisterDaemonRuntime(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            fixture.OrgID,
		MachineID:        fixture.MachineID,
		DaemonTokenID:    replacementToken.Record.ID,
		DaemonInstanceID: fixture.DaemonID,
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
	})
	if err != nil {
		t.Fatalf("transfer daemon runtime authority: %v", err)
	}
	if refreshed.ID != fixture.RuntimeID || refreshed.DaemonTokenID != replacementToken.Record.ID {
		t.Fatalf("refreshed runtime = %+v, want same runtime with replacement token", refreshed)
	}

	oldAuthority := fixture.authority()
	if offers, err := fixture.Store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{Authority: oldAuthority, Limit: 10},
	); err != nil || len(offers) != 0 {
		t.Fatalf("old token offers after transfer = %+v err=%v, want none", offers, err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: oldAuthority,
		ProcessID: process.ID,
	}); err != nil || found {
		t.Fatalf("old token accept after transfer found=%v err=%v, want rejected", found, err)
	}
	if _, err := fixture.Store.Execution().HeartbeatDaemonRuntime(ctx, executionstore.DaemonRuntimeLeaseInput{
		Authority:        oldAuthority,
		DaemonInstanceID: fixture.DaemonID,
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
	}); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("old token heartbeat after transfer error = %v, want unregistered", err)
	}
	if _, err := fixture.Store.Execution().EndDaemonRuntime(ctx, oldAuthority); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("old token end after transfer error = %v, want unregistered", err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       oldAuthority,
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		SourceStartedAt: fixture.Now.Add(time.Second),
	}); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("old token process report after transfer error = %v, want unregistered", err)
	}

	if _, found, err := fixture.Store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: replacementAuthority,
		ProcessID: process.ID,
	}); err != nil || !found {
		t.Fatalf("replacement token accept after transfer found=%v err=%v, want accepted", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       replacementAuthority,
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		SourceStartedAt: fixture.Now.Add(time.Second),
	}); err != nil {
		t.Fatalf("replacement token process report after transfer: %v", err)
	}
	actionToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_credential_authority_action",
		"write_process",
	)
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello"}`),
	})
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	if offers, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{Authority: oldAuthority, Limit: 10},
	); err != nil || len(offers) != 0 {
		t.Fatalf("old token action offers after transfer = %+v err=%v, want none", offers, err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(ctx, executionstore.AcceptDaemonProcessActionInput{
		Authority: oldAuthority,
		ProcessID: process.ID,
		ID:        action.ID,
	}); err != nil || found {
		t.Fatalf("old token action accept after transfer found=%v err=%v, want rejected", found, err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(ctx, executionstore.AcceptDaemonProcessActionInput{
		Authority: replacementAuthority,
		ProcessID: process.ID,
		ID:        action.ID,
	}); err != nil || !found {
		t.Fatalf("replacement token action accept after transfer found=%v err=%v, want accepted", found, err)
	}
	report := executionstore.CompleteDaemonProcessActionInput{
		Authority: replacementAuthority,
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ProcessID: process.ID,
		ID:        action.ID,
		Result:    json.RawMessage(`{}`),
	}
	oldReport := report
	oldReport.Authority = oldAuthority
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(ctx, oldReport); !errors.Is(
		err,
		storeerr.ErrDaemonRuntimeUnregistered,
	) {
		t.Fatalf("old token action report after transfer error = %v, want unregistered", err)
	}
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(ctx, report); err != nil {
		t.Fatalf("replacement token action report after transfer: %v", err)
	}
}

func TestDaemonRuntimeCredentialTransferFencesConcurrentOldTokenWork(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_credential_transfer_contention")
	activeProcess, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"runtime_credential_transfer_active",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
	})
	if err != nil {
		t.Fatalf("start active process: %v", err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: fixture.authority(),
		ProcessID: activeProcess.ID,
	}); err != nil || !found {
		t.Fatalf("accept active process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       fixture.authority(),
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              activeProcess.ID,
		SourceStartedAt: fixture.Now.Add(time.Second),
	}); err != nil {
		t.Fatalf("mark active process started: %v", err)
	}
	queuedProcess, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"runtime_credential_transfer_queued",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo queued",
		ShellSelector:         "sh",
	})
	if err != nil {
		t.Fatalf("start queued process: %v", err)
	}
	replacementToken, err := fixture.Store.Execution().CreateBYOMachineDaemonToken(ctx, executionstore.CreateBYOMachineDaemonTokenInput{
		OrgID:     fixture.OrgID,
		MachineID: fixture.MachineID,
		Name:      "contended-replacement-authority",
	})
	if err != nil {
		t.Fatalf("create replacement daemon token: %v", err)
	}

	agentBlocker, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin credential transfer agent blocker: %v", err)
	}
	defer func() { _ = agentBlocker.Rollback(ctx) }()
	if _, err := agentBlocker.Exec(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("lock agent for credential transfer: %v", err)
	}

	const applicationName = "daemon-credential-transfer-contention"
	writerConfig := fixture.Store.pool.Config()
	writerConfig.MaxConns = 1
	writerConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	writerPool, err := pgxpool.NewWithConfig(ctx, writerConfig)
	if err != nil {
		t.Fatalf("open credential transfer writer pool: %v", err)
	}
	t.Cleanup(writerPool.Close)
	type registrationResult struct {
		record executionstore.DaemonRuntimeRegistrationRecord
		err    error
	}
	registrationDone := make(chan registrationResult, 1)
	go func() {
		record, registerErr := newIntegrationStore(writerPool).Execution().RegisterDaemonRuntimeWithReconciliation(
			context.Background(),
			executionstore.RegisterDaemonRuntimeInput{
				OrgID:            fixture.OrgID,
				MachineID:        fixture.MachineID,
				DaemonTokenID:    replacementToken.Record.ID,
				DaemonInstanceID: fixture.DaemonID,
				DaemonVersion:    "1.0.0",
				LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
				ProcessClaims: []executionstore.ProcessReconciliationClaim{
					liveProcessReconciliationClaimForTest(activeProcess.ID),
				},
			},
		)
		registrationDone <- registrationResult{record: record, err: registerErr}
	}()
	integrationdb.WaitForApplicationLockWaiter(t, ctx, fixture.Store.pool, applicationName)

	type acceptResult struct {
		found bool
		err   error
	}
	acceptDone := make(chan acceptResult, 1)
	go func() {
		_, found, acceptErr := fixture.Store.Execution().AcceptDaemonProcess(
			context.Background(),
			executionstore.AcceptDaemonProcessInput{
				Authority: fixture.authority(),
				ProcessID: queuedProcess.ID,
			},
		)
		acceptDone <- acceptResult{found: found, err: acceptErr}
	}()
	reportDone := make(chan error, 1)
	go func() {
		_, reportErr := fixture.Store.Execution().MarkProcessStarted(context.Background(), executionstore.MarkProcessStartedInput{
			Authority:       fixture.authority(),
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              activeProcess.ID,
			SourceStartedAt: fixture.Now.Add(time.Second),
		})
		reportDone <- reportErr
	}()
	integrationdb.WaitForNamedLockWaiters(
		t,
		ctx,
		fixture.Store.pool,
		"LockMachineForRuntimeRegistration",
		2,
	)
	if err := agentBlocker.Commit(ctx); err != nil {
		t.Fatalf("release credential transfer agent blocker: %v", err)
	}
	registration := <-registrationDone
	if registration.err != nil {
		t.Fatalf("transfer daemon credential under contention: %v", registration.err)
	}
	if registration.record.Runtime.ID != fixture.RuntimeID ||
		registration.record.Runtime.DaemonTokenID != replacementToken.Record.ID {
		t.Fatalf("credential transfer registration = %+v", registration.record.Runtime)
	}
	accepted := <-acceptDone
	if accepted.err != nil || accepted.found {
		t.Fatalf("old token accept after concurrent transfer found=%v err=%v", accepted.found, accepted.err)
	}
	if err := <-reportDone; !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("old token report after concurrent transfer error = %v, want unregistered", err)
	}
}

func TestMachineRuntimeLifecycleOperationsDoNotDeadlockUnderContention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_runtime_lock_contention")
	expireDaemonRuntimeForTest(t, ctx, fixture)
	contendedCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	go func() {
		_, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(contendedCtx, 10)
		errs <- err
	}()
	go func() {
		_, err := fixture.Store.Execution().RevokeBYOMachineDaemonToken(
			contendedCtx,
			testOrgID,
			fixture.MachineID,
			fixture.TokenID,
			"contended_revoke")

		if storeerr.IsNotFound(err) {
			err = nil
		}
		errs <- err
	}()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("contended machine/runtime lifecycle operation failed: %v", err)
		}
	}
}

func TestFreshDaemonRuntimePredicateBlocksStaleActiveRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "stale_active_runtime")

	expireDaemonRuntimeForTest(t, ctx, fixture)

	_, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    createToolCallForProcessTest(t, ctx, fixture, "stale_active_runtime_start", "run_command"),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "bash",
		ShellSelector:         "sh",
	})
	if !errors.Is(err, storeerr.ErrMachineNotReachable) {
		t.Fatalf("start process with stale active daemon runtime error = %v, want ErrMachineNotReachable", err)
	}
}

func TestRuntimeRegistrationReoffersTerminalRead(t *testing.T) {
	t.Parallel()
	for _, accepted := range []bool{false, true} {
		name := "queued"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newProcessDaemonFixture(
				t,
				ctx,
				"terminal_read_reconnect_"+name,
			)
			process, action, toolCallID :=
				createTerminalProcessActionForLifecycleTest(
					t,
					ctx,
					fixture,
					"terminal_read_reconnect_"+name,
					"read_process",
					executionstore.ProcessActionKindRead,
					accepted,
				)
			registration, err :=
				fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
					ctx,
					executionstore.RegisterDaemonRuntimeInput{
						OrgID:            fixture.OrgID,
						MachineID:        fixture.MachineID,
						DaemonTokenID:    fixture.TokenID,
						DaemonInstanceID: testID(t.Name() + "-replacement"),
						DaemonVersion:    "1.0.0",
						LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
						ProcessClaims: []executionstore.ProcessReconciliationClaim{
							terminalProcessReconciliationClaimForTest(process.ID),
						},
					},
				)
			if err != nil {
				t.Fatalf("register replacement runtime: %v", err)
			}
			disposition := processReconciliationDirectiveForTest(
				t,
				registration.Reconciliation,
				process.ID,
			)
			if disposition.Disposition !=
				daemonprotocol.ProcessDispositionRelease ||
				len(disposition.Actions) != 0 {
				t.Fatalf("terminal process disposition = %+v", disposition)
			}
			updated, found, err :=
				fixture.Store.Execution().GetProcessActionByToolCall(
					ctx,
					testProjectID,
					fixture.AgentID,
					toolCallID,
				)
			if err != nil {
				t.Fatalf("get terminal read action: %v", err)
			}
			if !found ||
				updated.ID != action.ID ||
				updated.State != executionstore.ProcessActionStateQueued ||
				updated.StateReasonCode != "" {
				t.Fatalf(
					"terminal read after reconnect = found %t %+v",
					found,
					updated,
				)
			}
			offers, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
				ctx,
				executionstore.DaemonWorkInput{
					Authority: fixture.authorityForRuntime(registration.Runtime.ID),
					Limit:     10,
				},
			)
			if err != nil {
				t.Fatalf("list terminal read offers: %v", err)
			}
			if len(offers) != 1 || offers[0].ID != action.ID {
				t.Fatalf("terminal read offers = %+v", offers)
			}
		})
	}
}

func TestRuntimeRegistrationRetainsTerminalMutationEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"terminal_mutation_reconnect",
	)
	process, action, toolCallID :=
		createTerminalProcessActionForLifecycleTest(
			t,
			ctx,
			fixture,
			"terminal_mutation_reconnect",
			"write_process",
			executionstore.ProcessActionKindWrite,
			true,
		)
	claim := terminalProcessReconciliationClaimForTest(process.ID)
	claim.Actions = []executionstore.ProcessActionReconciliationClaim{{
		ProcessActionID: action.ID,
		Seq:             action.Seq,
		ActionKind:      action.ActionKind,
		Position:        daemonprotocol.ActionPositionTerminal,
	}}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims:    []executionstore.ProcessReconciliationClaim{claim},
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRelease ||
		len(disposition.Actions) != 1 ||
		disposition.Actions[0].ProcessActionID != action.ID ||
		disposition.Actions[0].Disposition !=
			daemonprotocol.ActionDispositionRetain {
		t.Fatalf("terminal mutation disposition = %+v", disposition)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get terminal mutation: %v", err)
	}
	if !found ||
		updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateAccepted ||
		updated.StateReasonCode != "" {
		t.Fatalf("terminal mutation after reconnect = found %t %+v", found, updated)
	}
}

func TestRuntimeRegistrationResolvesTerminalMutationWithoutLocalState(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"terminal_mutation_missing_local",
	)
	process, action, toolCallID :=
		createTerminalProcessActionForLifecycleTest(
			t,
			ctx,
			fixture,
			"terminal_mutation_missing_local",
			"write_process",
			executionstore.ProcessActionKindWrite,
			true,
		)
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get terminal mutation: %v", err)
	}
	if !found ||
		updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateUnknown ||
		updated.StateReasonCode !=
			executionstore.LocalProcessMissingAfterDaemonReconnectReason {
		t.Fatalf("terminal mutation after reconnect = found %t %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get terminal mutation tool call: %v", err)
	}
	assertCompletedProcessActionResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.ProcessActionStateUnknown,
	)

	readToolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"terminal_mutation_missing_local_read",
	)
	read, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    readToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create read after missing mutation resolution: %v", err)
	}
	offers, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authorityForRuntime(registration.Runtime.ID),
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list reads after mutation resolution: %v", err)
	}
	if len(offers) != 1 || offers[0].ID != read.ID {
		t.Fatalf("read offers after mutation resolution = %+v", offers)
	}
}

func TestReplacementRuntimeWithoutProcessClaimClosesQueuedReadAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_queued_unknown")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_queued_unknown",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runtime_queued_unknown_process", "run_command"),
			builtInProcessToolCallBatchItem("runtime_queued_unknown", "read_process"),
		},
	)
	processToolCallID, toolCallID := toolCallIDs[0], toolCallIDs[1]

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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0,"wait_ms":1000}`),
	})
	if err != nil {
		t.Fatalf("create queued read action: %v", err)
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get action by tool call: %v", err)
	}
	if !found {
		t.Fatal("expected queued action to remain addressable")
	}
	if updated.ID != action.ID || updated.State != executionstore.ProcessActionStateFailed ||
		updated.StateReasonCode != executionstore.LocalProcessMissingAfterDaemonReconnectReason {
		t.Fatalf("expected queued action failed from missing local process state, got %+v", updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.LocalProcessMissingAfterDaemonReconnectReason,
	)
	assertProcessActionToolResultUsesPublicIDs(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall.TurnID,
		toolCall.ID,
		process.ID,
		action.ID,
	)
}

func TestReplacementDaemonRuntimeRoutesLiveProcessClaimByMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_reconcile_process")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_process",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runtime_reconcile_process_start", "run_command"),
			builtInProcessToolCallBatchItem("runtime_reconcile_process", "read_process"),
		},
	)
	processToolCallID, toolCallID := toolCallIDs[0], toolCallIDs[1]

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
	processGrant, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID)

	if err != nil {
		t.Fatalf("accept process: %v", err)
	}
	if !found {
		t.Fatal("expected process accept")
	}
	if processGrant.Process.ExecutionGrantedAt == nil {
		t.Fatal("accepted process is missing execution grant time")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create queued read action: %v", err)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{
				liveProcessReconciliationClaimForTest(process.ID),
			},
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime with a process claim: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain {
		t.Fatalf("process reconciliation directive = %+v, want retain", disposition)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get reconciled process: %v", err)
	}
	if updated.State != executionstore.ProcessStateRunning ||
		updated.ExecutionGrantedAt == nil ||
		!updated.ExecutionGrantedAt.Equal(
			*processGrant.Process.ExecutionGrantedAt,
		) {
		t.Fatalf("reconciled process = %+v, want running", updated)
	}
	accepted, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		registration.Runtime.ID,
		process.ID,
		NilID)

	if err != nil {
		t.Fatalf("accept process action through replacement runtime: %v", err)
	}
	if !found {
		t.Fatal("expected queued action to accept after reconnect")
	}
	if accepted.ID != action.ID || accepted.State != executionstore.ProcessActionStateAccepted {
		t.Fatalf("accepted action = %+v, want original queued action %s accepted", accepted, action.ID)
	}
}

func TestReplacementDaemonRuntimeRedeliversAcceptedActionMissingLocally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_reconcile_accepted_action")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_accepted_action",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runtime_reconcile_accepted_action_start", "run_command"),
			builtInProcessToolCallBatchItem("runtime_reconcile_accepted_action", "read_process"),
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello"}`),
	})
	if err != nil {
		t.Fatalf("create accepted action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action accept")
	}
	offers, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authority(),
			Limit:     1,
		},
	)
	if err != nil {
		t.Fatalf("list offers after action acceptance: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf(
			"accepted action returned through normal offers: %+v",
			offers,
		)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{
				liveProcessReconciliationClaimForTest(process.ID),
			},
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime with a process claim: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain ||
		len(disposition.Actions) != 1 ||
		disposition.Actions[0].ProcessActionID != action.ID ||
		disposition.Actions[0].Disposition != daemonprotocol.ActionDispositionApply {
		t.Fatalf("reconciliation disposition = %+v, want retain plus action apply", disposition)
	}
	reconciled, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get reconciled process: %v", err)
	}
	if reconciled.State != executionstore.ProcessStateRunning ||
		reconciled.ExecutionGrantedAt == nil {
		t.Fatalf("process after reconnect = %+v, want granted running process", reconciled)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get reconciled action: %v", err)
	}
	if !found || updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateAccepted {
		t.Fatalf("accepted action after reconnect = found %v %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
		t.Fatalf("redelivered action tool call = %+v, want waiting", toolCall)
	}
	replacementAuthority := fixture.authorityForRuntime(registration.Runtime.ID)
	application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			Authority: replacementAuthority,
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
		},
	)
	if err != nil {
		t.Fatalf("report inherited action from replacement runtime: %v", err)
	}
	if application.Action.State != executionstore.ProcessActionStateApplied ||
		!application.ToolResultCommitted {
		t.Fatalf("inherited action application = %+v", application)
	}
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
		},
	); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("late action report from replaced runtime error = %v, want ErrDaemonRuntimeUnregistered", err)
	}
}

func TestReplacementDaemonRuntimeReleasesCommittedReadBeforeLaterMutation(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_reconcile_read_lost_ack")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_read_lost_ack",
		nil,
	)

	readToolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_read_lost_ack_read",
	)
	read, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    readToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create read: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		read.ID); err != nil || !found {
		t.Fatalf("accept read found=%t err=%v", found, err)
	}
	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
	if application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        read.ID,
			Authority: fixture.authority(),
			Result: json.RawMessage(
				`{"process_id":"` + publicProcessID +
					`","output":"ready","cursor":0,"next_cursor":5,"truncated":false}`,
			),
		},
	); err != nil || !application.ToolResultCommitted {
		t.Fatalf("commit read before lost acknowledgement = %+v err=%v", application, err)
	}

	writeToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_read_lost_ack_write",
		"write_process",
	)
	write, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    writeToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindWrite,
			Payload:    json.RawMessage(`{"data":"next\n"}`),
		},
	)
	if err != nil {
		t.Fatalf("create later write: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		write.ID); err != nil || !found {
		t.Fatalf("accept later write found=%t err=%v", found, err)
	}

	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{
				liveProcessReconciliationClaimForTest(
					process.ID,
					executionstore.ProcessActionReconciliationClaim{
						ProcessActionID: write.ID,
						Seq:             write.Seq,
						ActionKind:      write.ActionKind,
						Position:        daemonprotocol.ActionPositionTerminal,
					},
				),
			},
		},
	)
	if err != nil {
		t.Fatalf("reconcile committed read with lost acknowledgement: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain ||
		len(disposition.Actions) != 2 ||
		disposition.Actions[0].ProcessActionID != read.ID ||
		disposition.Actions[0].Disposition != daemonprotocol.ActionDispositionRelease ||
		disposition.Actions[1].ProcessActionID != write.ID ||
		disposition.Actions[1].Disposition != daemonprotocol.ActionDispositionRetain {
		t.Fatalf(
			"reconciliation disposition = %+v, want read release before later write retention",
			disposition,
		)
	}
}

func TestReplacementDaemonRuntimeReleasesServerResolvedTerminate(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"runtime_reconcile_server_resolved_terminate",
	)
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_server_resolved_terminate",
		nil,
	)

	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_server_resolved_terminate_actions",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runtime_reconcile_server_resolved_terminate_write", "write_process"),
			builtInProcessToolCallBatchItem("runtime_reconcile_server_resolved_terminate_stop", "stop_process"),
		},
	)
	writeToolCallID, terminateToolCallID := toolCallIDs[0], toolCallIDs[1]
	write, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    writeToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindWrite,
			Payload:    json.RawMessage(`{"data":"before exit\n"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		write.ID); err != nil || !found {
		t.Fatalf("accept write found=%t err=%v", found, err)
	}

	terminate, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    terminateToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindTerminate,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			SourceEndedAt: fixture.Now.Add(6 * time.Second),
		},
	); err != nil {
		t.Fatal(err)
	}
	resolvedTerminate, found, err :=
		fixture.Store.Execution().GetProcessActionByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			terminateToolCallID,
		)
	if err != nil ||
		!found ||
		resolvedTerminate.ID != terminate.ID ||
		resolvedTerminate.State != executionstore.ProcessActionStateApplied ||
		resolvedTerminate.StateReasonCode != "already_stopped" {
		t.Fatalf(
			"server-resolved terminate = found %t %+v err=%v",
			found,
			resolvedTerminate,
			err,
		)
	}

	claim := terminalProcessReconciliationClaimForTest(process.ID)
	claim.Actions = []executionstore.ProcessActionReconciliationClaim{{
		ProcessActionID: write.ID,
		Seq:             write.Seq,
		ActionKind:      write.ActionKind,
		Position:        daemonprotocol.ActionPositionTerminal,
	}}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims:    []executionstore.ProcessReconciliationClaim{claim},
		},
	)
	if err != nil {
		t.Fatalf("reconcile server-resolved terminate: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRelease ||
		len(disposition.Actions) != 1 ||
		disposition.Actions[0].ProcessActionID != write.ID ||
		disposition.Actions[0].Disposition != daemonprotocol.ActionDispositionRetain {
		t.Fatalf(
			"reconciliation disposition = %+v, want only the local write retention",
			disposition,
		)
	}
}

func TestReplacementDaemonRuntimeSettlesAlreadyAppliedActionEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_reconcile_action_report")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_reconcile_action_report",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runtime_reconcile_action_report_start", "run_command"),
			builtInProcessToolCallBatchItem("runtime_reconcile_action_report", "read_process"),
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	})
	if err != nil {
		t.Fatalf("create accepted action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		action.ID); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action accept")
	}
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
			Authority: fixture.authority(),
		},
	); err != nil {
		t.Fatalf("apply action before reconnect: %v", err)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            fixture.OrgID,
		MachineID:        fixture.MachineID,
		DaemonTokenID:    fixture.TokenID,
		DaemonInstanceID: testID(t.Name() + "-replacement"),
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		ProcessClaims: []executionstore.ProcessReconciliationClaim{
			liveProcessReconciliationClaimForTest(
				process.ID,
				executionstore.ProcessActionReconciliationClaim{
					ProcessActionID: action.ID,
					Seq:             action.Seq,
					ActionKind:      action.ActionKind,
					Position:        daemonprotocol.ActionPositionTerminal,
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("register replacement runtime with action report: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain ||
		len(disposition.Actions) != 1 ||
		disposition.Actions[0].ProcessActionID != action.ID ||
		disposition.Actions[0].Disposition != daemonprotocol.ActionDispositionSettle {
		t.Fatalf("reconciliation disposition = %+v, want retain plus settle", disposition)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action by tool call: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateApplied {
		t.Fatalf(
			"action after registration report = found %v %+v, want applied",
			found,
			updated,
		)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action tool call: %v", err)
	}
	assertCompletedProcessActionResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessActionStateApplied)
	reconciled, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get reconciled process: %v", err)
	}
	if reconciled.State != executionstore.ProcessStateRunning ||
		reconciled.ExecutionGrantedAt == nil {
		t.Fatalf("process after action report reconciliation = %+v, want granted running process", reconciled)
	}
}

func TestReplacementDaemonRuntimeSettlesFailedActionEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "replacement_failed_action_report")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"replacement_failed_action_report",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("replacement_failed_action_report_start", "run_command"),
			builtInProcessToolCallBatchItem("replacement_failed_action_report", "read_process"),
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	})
	if err != nil {
		t.Fatalf("create action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		action.ID); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action accept")
	}
	if _, err := fixture.Store.Execution().FailDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ProcessID:          process.ID,
			ID:                 action.ID,
			Authority:          fixture.authority(),
			StateReasonCode:    "write_failed",
			StateReasonMessage: "write failed after acceptance",
		},
	); err != nil {
		t.Fatalf("fail action before reconnect: %v", err)
	}

	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            fixture.OrgID,
		MachineID:        fixture.MachineID,
		DaemonTokenID:    fixture.TokenID,
		DaemonInstanceID: testID(t.Name() + "-replacement"),
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		ProcessClaims: []executionstore.ProcessReconciliationClaim{
			liveProcessReconciliationClaimForTest(
				process.ID,
				executionstore.ProcessActionReconciliationClaim{
					ProcessActionID: action.ID,
					Seq:             action.Seq,
					ActionKind:      action.ActionKind,
					Position:        daemonprotocol.ActionPositionTerminal,
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("register replacement runtime with failed action report: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain ||
		len(disposition.Actions) != 1 ||
		disposition.Actions[0].ProcessActionID != action.ID ||
		disposition.Actions[0].Disposition != daemonprotocol.ActionDispositionSettle {
		t.Fatalf("reconciliation disposition = %+v, want retain plus settle", disposition)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action by tool call: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateFailed ||
		updated.StateReasonCode != "write_failed" {
		t.Fatalf("failed action report = found %v %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action tool call: %v", err)
	}
	assertCompletedProcessActionResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessActionStateFailed)
}

func TestReplacementDaemonRuntimeReportsTerminalActionGrantedByExpiredRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_expired_action_report")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_expired_action_report",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runtime_expired_action_report_start", "run_command"),
			builtInProcessToolCallBatchItem("runtime_expired_action_report", "read_process"),
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	})
	if err != nil {
		t.Fatalf("create accepted action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		action.ID); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action accept")
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	ended, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(ctx, 10)
	if err != nil {
		t.Fatalf("end expired daemon runtimes: %v", err)
	}
	if len(ended) != 1 || ended[0].ID != fixture.RuntimeID {
		t.Fatalf("ended expired daemon runtimes = %+v, want %s", ended, fixture.RuntimeID)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            fixture.OrgID,
		MachineID:        fixture.MachineID,
		DaemonTokenID:    fixture.TokenID,
		DaemonInstanceID: testID(t.Name() + "-replacement"),
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		ProcessClaims: []executionstore.ProcessReconciliationClaim{
			liveProcessReconciliationClaimForTest(
				process.ID,
				executionstore.ProcessActionReconciliationClaim{
					ProcessActionID: action.ID,
					Seq:             action.Seq,
					ActionKind:      action.ActionKind,
					Position:        daemonprotocol.ActionPositionTerminal,
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("register replacement runtime with expired-runtime action report: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain ||
		len(disposition.Actions) != 1 ||
		disposition.Actions[0].ProcessActionID != action.ID ||
		disposition.Actions[0].Disposition != daemonprotocol.ActionDispositionRetain {
		t.Fatalf("reconciliation disposition = %+v, want retained terminal evidence", disposition)
	}
	application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			Authority: fixture.authorityForRuntime(registration.Runtime.ID),
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
		},
	)
	if err != nil {
		t.Fatalf("report retained terminal action from replacement runtime: %v", err)
	}
	if !application.ToolResultCommitted {
		t.Fatalf("retained terminal action application = %+v", application)
	}
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
		},
	); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("late retained-action report from expired runtime error = %v, want ErrDaemonRuntimeUnregistered", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action by tool call: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateApplied {
		t.Fatalf(
			"expired-runtime action report = found %v %+v, want applied",
			found,
			updated,
		)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action tool call: %v", err)
	}
	assertCompletedProcessActionResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessActionStateApplied)
}

func TestDaemonRuntimeEndPreservesLiveProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "daemon_runtime_released_process")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"daemon_runtime_released_process_start",
			"run_command",
		),
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
	grant, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID)

	if err != nil {
		t.Fatalf("accept process: %v", err)
	}
	if !found {
		t.Fatal("expected process accept")
	}
	if grant.Process.ExecutionGrantedAt == nil {
		t.Fatal("accepted process is missing execution grant time")
	}
	ended, err := fixture.Store.Execution().EndDaemonRuntime(
		ctx,
		fixture.authority(),
	)
	if err != nil {
		t.Fatalf("end daemon runtime: %v", err)
	}
	if ended.StateReasonCode != executionstore.DaemonRuntimeReleasedReason {
		t.Fatalf("runtime end reason = %q, want %q", ended.StateReasonCode, executionstore.DaemonRuntimeReleasedReason)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after daemon shutdown: %v", err)
	}
	if (updated.State != executionstore.ProcessStateStarting &&
		updated.State != executionstore.ProcessStateRunning) ||
		updated.ExecutionGrantedAt == nil ||
		!updated.ExecutionGrantedAt.Equal(*grant.Process.ExecutionGrantedAt) {
		t.Fatalf("daemon runtime end should not mark process terminal, got %+v", updated)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{
				liveProcessReconciliationClaimForTest(process.ID),
			},
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain {
		t.Fatalf("expected retained process after daemon shutdown, got %+v", disposition)
	}
}

func TestExpiredDaemonRuntimeHeartbeatRestoresSameRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "expired_runtime_process")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"expired_runtime_process_start",
			"run_command",
		),
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
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOffline,
	)
	afterExpiry, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after lease expiry: %v", err)
	}
	if afterExpiry.State != executionstore.ProcessStateStarting && afterExpiry.State != executionstore.ProcessStateRunning {
		t.Fatalf("lease expiry should not terminalize process, got %+v", afterExpiry)
	}
	heartbeat, err := fixture.Store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority:        fixture.authority(),
			DaemonInstanceID: fixture.DaemonID,
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("heartbeat expired runtime with same daemon instance: %v", err)
	}
	if heartbeat.ID != fixture.RuntimeID {
		t.Fatalf("heartbeat runtime id = %s, want same runtime %s", heartbeat.ID, fixture.RuntimeID)
	}
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOnline,
	)
}

func TestExpiredDaemonRuntimeRegistrationRefreshesSameRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "expired_runtime_register_same")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"expired_runtime_register_same_start",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{
				liveProcessReconciliationClaimForTest(process.ID),
			},
		},
	)
	if err != nil {
		t.Fatalf("register same daemon instance after expiry: %v", err)
	}
	if registration.Runtime.ID != fixture.RuntimeID {
		t.Fatalf("registration runtime id = %s, want same runtime %s", registration.Runtime.ID, fixture.RuntimeID)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRetain {
		t.Fatalf("same daemon instance refresh disposition = %+v, want retain", disposition)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after same instance registration: %v", err)
	}
	if updated.State == executionstore.ProcessStateUnknown {
		t.Fatalf("same daemon instance registration should preserve process state, got %+v", updated)
	}
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOnline,
	)
}

func TestEndedExpiredDaemonRuntimeRegistrationReactivatesSameRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "ended_expired_runtime_register_same")
	expireDaemonRuntimeForTest(t, ctx, fixture)
	ended, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(ctx, 10)
	if err != nil {
		t.Fatalf("end expired daemon runtime: %v", err)
	}
	if len(ended) != 1 || ended[0].ID != fixture.RuntimeID {
		t.Fatalf("ended daemon runtimes = %+v, want %s", ended, fixture.RuntimeID)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("register ended lease-expired daemon instance: %v", err)
	}
	if registration.Runtime.ID != fixture.RuntimeID || registration.Runtime.State != "active" ||
		registration.Runtime.StateReasonCode != "" || registration.Runtime.EndedAt != nil {
		t.Fatalf("reactivated runtime = %+v, want active runtime %s", registration.Runtime, fixture.RuntimeID)
	}
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOnline,
	)
}

func TestSupersededDaemonInstanceCannotReplaceCurrentRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "superseded_instance_registration")
	replacementInstanceID := testID(t.Name() + "-replacement")
	replacement, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: replacementInstanceID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("register replacement daemon instance: %v", err)
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); !errors.Is(err, storeerr.ErrDaemonInstanceSuperseded) {
		t.Fatalf("superseded daemon registration error = %v, want ErrDaemonInstanceSuperseded", err)
	}
	current, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: replacementInstanceID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("refresh replacement daemon instance: %v", err)
	}
	if current.ID != replacement.ID {
		t.Fatalf("current runtime id = %s, want replacement runtime %s", current.ID, replacement.ID)
	}
}

func TestEndExpiredDaemonRuntimesEndsRuntimeWithoutUnknowningRecoverableWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "expired_runtime_gc")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    createToolCallForProcessTest(t, ctx, fixture, "expired_runtime_gc_start", "run_command"),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	expireDaemonRuntimeForTest(t, ctx, fixture)

	ended, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(ctx, 10)
	if err != nil {
		t.Fatalf("end expired daemon runtimes: %v", err)
	}
	if len(ended) != 1 || ended[0].ID != fixture.RuntimeID || ended[0].State != "ended" {
		t.Fatalf("ended daemon runtimes = %+v, want ended runtime %s", ended, fixture.RuntimeID)
	}
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOffline,
	)
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after expired runtime GC: %v", err)
	}
	if updated.State != executionstore.ProcessStateRunning ||
		updated.ExecutionGrantedAt == nil {
		t.Fatalf("process after expired runtime GC = %+v, want recoverable granted process", updated)
	}
}

func TestReplacementDaemonRuntimeMarksUnclaimedGrantedProcessUnknownAfterLeaseExpiry(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "expired_runtime_missing_local")

	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "expired_runtime_missing_local_start", "run_command")
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	grant, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID)

	if err != nil {
		t.Fatalf("accept process: %v", err)
	}
	if !found {
		t.Fatal("expected process accept")
	}
	if grant.Process.ExecutionGrantedAt == nil {
		t.Fatal("accepted process is missing execution grant time")
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOffline,
	)
	afterExpiry, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after lease expiry: %v", err)
	}
	if afterExpiry.State == executionstore.ProcessStateUnknown {
		t.Fatalf("lease expiry should not mark process unknown before daemon reconciliation: %+v", afterExpiry)
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement-expired"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after empty replacement reconciliation: %v", err)
	}
	if updated.State != executionstore.ProcessStateUnknown ||
		updated.StateReasonCode != "local_process_missing_after_daemon_reconnect" ||
		updated.ExecutionGrantedAt == nil ||
		!updated.ExecutionGrantedAt.Equal(*grant.Process.ExecutionGrantedAt) {
		t.Fatalf(
			"process after empty replacement reconciliation = %+v, want unknown local_process_missing_after_daemon_reconnect",
			updated,
		)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"local_process_missing_after_daemon_reconnect",
	)
}

func TestExpiredDaemonRuntimeLeasePreservesUnacceptedProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "expired_runtime_queued_process")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"expired_runtime_queued_process_start",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo hi",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	assertMachineState(
		t, ctx, fixture.Store, fixture.MachineID,
		executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOffline,
	)
	afterExpiry, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after lease expiry: %v", err)
	}
	if afterExpiry.State != executionstore.ProcessStateQueued ||
		afterExpiry.ExecutionGrantedAt != nil {
		t.Fatalf("lease expiry should leave unaccepted process queued, got %+v", afterExpiry)
	}
	if _, err := fixture.Store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority:        fixture.authority(),
			DaemonInstanceID: fixture.DaemonID,
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("heartbeat expired runtime with same daemon instance: %v", err)
	}
	accepted, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID)

	if err != nil {
		t.Fatalf("accept queued process after runtime heartbeat: %v", err)
	}
	if !found || accepted.Process.ID != process.ID ||
		accepted.Process.State != executionstore.ProcessStateStarting ||
		accepted.Process.ExecutionGrantedAt == nil {
		t.Fatalf("replacement runtime accept found=%v process=%+v want %s", found, accepted, process.ID)
	}
}

func TestTerminalReportBeforeReconnectCompletesToolCallWithOutputResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal_report_output")
	toolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "terminal_report_output")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo terminal",
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	exitCode := 0
	result := json.RawMessage(`{"output":"terminal-output","cursor":0,"next_cursor":15,"truncated":false,"done":true,"process_id":"prc_untrusted"}`)
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			State:           executionstore.ProcessStateExited,
			ExitCode:        &exitCode,
			Result:          result,
			SourceStartedAt: fixture.Now.Add(time.Second),
			SourceEndedAt:   fixture.Now.Add(2 * time.Second),
		},
	); err != nil {
		t.Fatalf("apply terminal report before reconnect: %v", err)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            fixture.OrgID,
		MachineID:        fixture.MachineID,
		DaemonTokenID:    fixture.TokenID,
		DaemonInstanceID: testID(t.Name() + "-replacement"),
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		ProcessClaims: []executionstore.ProcessReconciliationClaim{
			terminalProcessReconciliationClaimForTest(process.ID),
		},
	})
	if err != nil {
		t.Fatalf("reconcile terminal process: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionRelease {
		t.Fatalf("terminal process directive = %+v, want release", disposition)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State != "completed" {
		t.Fatalf("tool call after terminal report = %+v", toolCall)
	}
	completed := completedToolCallForTest(t, fixture.Store, fixture.AgentID, toolCall.TurnID, toolCall.ID)
	if !strings.Contains(string(completed.ResultContentParts), "terminal-output") {
		t.Fatalf("typed terminal report tool result = %s, want terminal-output", completed.ResultContentParts)
	}
	if strings.Contains(string(completed.ResultContentParts), "prc_untrusted") {
		t.Fatalf("terminal reconciliation tool result leaked process_id: %s", completed.ResultContentParts)
	}
	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
	if !strings.Contains(string(completed.ResultContentParts), publicProcessID) {
		t.Fatalf("terminal reconciliation result lacks process handle: %s", completed.ResultContentParts)
	}
}

func TestRuntimeRegistrationReconciliationLocksAgentBeforeProcessAndReadMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_reconciliation_agent_lock_order")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "runtime_reconciliation_agent_lock_order", "run_command")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo lock",
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(
		t,
		ctx,
		fixture,
		process,
		fixture.Now.Add(1500*time.Millisecond),
	)
	readToolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"runtime_reconciliation_agent_lock_order_read",
	)
	read, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    readToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"wait_ms":1000}`),
		},
	)
	if err != nil {
		t.Fatalf("create process read: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		read.ID); err != nil {
		t.Fatalf("accept process read: %v", err)
	} else if !found {
		t.Fatal("expected process read accept")
	}
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock holder: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(
		ctx,
		`SELECT 1 FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("lock agent row: %v", err)
	}

	const applicationName = "runtime-reconciliation-agent-lock-order"
	writerConfig := fixture.Store.pool.Config()
	writerConfig.MaxConns = 1
	writerConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	writerPool, err := pgxpool.NewWithConfig(ctx, writerConfig)
	if err != nil {
		t.Fatalf("open registration pool: %v", err)
	}
	t.Cleanup(writerPool.Close)

	done := make(chan error, 1)
	go func() {
		_, err := newIntegrationStore(writerPool).Execution().RegisterDaemonRuntimeWithReconciliation(context.Background(), executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		})
		done <- err
	}()

	integrationdb.WaitForApplicationLockWaiter(
		t,
		ctx,
		fixture.Store.pool,
		applicationName,
	)

	var processState executionstore.ProcessState
	var actionState executionstore.ProcessActionState
	if err := tx.QueryRow(
		ctx,
		`SELECT process.state, action.state
		 FROM processes process
		 JOIN process_actions action ON action.process_id = process.id
		 WHERE process.id = $1 AND action.id = $2
		 FOR UPDATE OF process, action NOWAIT`,
		process.ID,
		read.ID,
	).Scan(&processState, &actionState); err != nil {
		t.Fatalf("registration locked process or read before agent: %v", err)
	}
	if processState != executionstore.ProcessStateRunning ||
		actionState != executionstore.ProcessActionStateAccepted {
		t.Fatalf(
			"process/read before agent unlock = %s/%s, want running/accepted",
			processState,
			actionState,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("release agent lock: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("reconciliation after agent release: %v", err)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get reconciled process: %v", err)
	}
	if updated.State != executionstore.ProcessStateUnknown ||
		updated.StateReasonCode != executionstore.LocalProcessMissingAfterDaemonReconnectReason {
		t.Fatalf("process after reconciliation = %+v, want unknown missing-local-state", updated)
	}
}

func TestRuntimeRegistrationClosesPreparationForStillQueuedProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "unaccepted_observations")
	toolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "unaccepted_observations")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo terminal",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            fixture.OrgID,
		MachineID:        fixture.MachineID,
		DaemonTokenID:    fixture.TokenID,
		DaemonInstanceID: testID(t.Name() + "-replacement"),
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		ProcessClaims: []executionstore.ProcessReconciliationClaim{{
			ProcessID:            process.ID,
			SupervisorInstanceID: "test-preparation-supervisor-instance",
			Phase:                daemonprotocol.ProcessPhasePrepared,
			SupervisorLive:       true,
		}},
	})
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition != daemonprotocol.ProcessDispositionClosePreparation {
		t.Fatalf("queued process reconciliation directive = %+v, want close preparation", disposition)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateQueued ||
		updated.ExecutionGrantedAt != nil {
		t.Fatalf("queued process after reconciliation = %+v, want ungranted queued process", updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State == "completed" || toolCall.State == "failed" || toolCall.CompletedAt != nil {
		t.Fatalf("queued tool call after reconciliation = %+v, want non-terminal", toolCall)
	}
}

func TestRuntimeRegistrationClosesPreparationAndResolvesAcceptedTerminalAction(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"terminal_preparation_accepted_action",
	)
	process, action, toolCallID :=
		createTerminalProcessActionForLifecycleTest(
			t,
			ctx,
			fixture,
			"terminal_preparation_accepted_action",
			"write_process",
			executionstore.ProcessActionKindWrite,
			true,
		)

	registration, err :=
		fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
			ctx,
			executionstore.RegisterDaemonRuntimeInput{
				OrgID:            fixture.OrgID,
				MachineID:        fixture.MachineID,
				DaemonTokenID:    fixture.TokenID,
				DaemonInstanceID: testID(t.Name() + "-replacement"),
				DaemonVersion:    "1.0.0",
				LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
				ProcessClaims: []executionstore.ProcessReconciliationClaim{{
					ProcessID:            process.ID,
					SupervisorInstanceID: "prepared-supervisor-instance",
					Phase:                daemonprotocol.ProcessPhasePrepared,
					SupervisorLive:       true,
				}},
			},
		)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	disposition := processReconciliationDirectiveForTest(
		t,
		registration.Reconciliation,
		process.ID,
	)
	if disposition.Disposition !=
		daemonprotocol.ProcessDispositionClosePreparation {
		t.Fatalf(
			"terminal preparation disposition = %+v, want close preparation",
			disposition,
		)
	}

	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get terminal mutation: %v", err)
	}
	if !found ||
		updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateUnknown ||
		updated.StateReasonCode !=
			executionstore.ProcessActionOutcomeUnrecoverableReason {
		t.Fatalf(
			"terminal mutation after preparation cleanup = found %t %+v",
			found,
			updated,
		)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get terminal mutation tool call: %v", err)
	}
	assertCompletedProcessActionResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.ProcessActionStateUnknown,
	)
}

func TestReplacementRuntimeWithoutProcessClaimFailsAcceptedRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_unknown")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_unknown",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runtime_unknown_process", "run_command"),
			builtInProcessToolCallBatchItem("runtime_unknown", "read_process"),
		},
	)
	processToolCallID, toolCallID := toolCallIDs[0], toolCallIDs[1]

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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0,"wait_ms":1000}`),
	})
	if err != nil {
		t.Fatalf("create read action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action accept")
	}
	replacement, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionForDaemonReport(
		ctx,
		fixture.authorityForRuntime(replacement.ID),
		process.ID,
		action.ID,
	)
	if err != nil {
		t.Fatalf("get action by id: %v", err)
	}
	if !found {
		t.Fatal("expected accepted action to remain addressable")
	}
	if updated.ID != action.ID || updated.State != executionstore.ProcessActionStateFailed ||
		updated.StateReasonCode != executionstore.LocalProcessMissingAfterDaemonReconnectReason {
		t.Fatalf("expected read failed from missing local process state, got %+v", updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.LocalProcessMissingAfterDaemonReconnectReason,
	)
	assertProcessActionToolResultUsesPublicIDs(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall.TurnID,
		toolCall.ID,
		process.ID,
		action.ID,
	)
}

func TestReplacementRuntimeWithoutProcessClaimClosesProcessUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_process_unknown")
	toolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "runtime_process_unknown")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateUnknown ||
		updated.StateReasonCode != executionstore.LocalProcessMissingAfterDaemonReconnectReason ||
		updated.ExecutionGrantedAt == nil {
		t.Fatalf("expected process unknown from missing local process state, got %+v", updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.LocalProcessMissingAfterDaemonReconnectReason,
	)
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		RuntimeLockID: fixture.Lock.ID,
		ToolCallID:    toolCallID}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	}); err == nil {
		t.Fatal("create process action should fail after missing local process state makes the process terminal")
	}
}

func TestExpiredAgentRuntimeLockFailsUnacceptedProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "stale_runtime_fails_unaccepted_process")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"stale_runtime_fails_unaccepted_process",
		"run_command",
	)

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}

	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, fixture.Lock.ID)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap stale runtime locks: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped stale locks = %d, want 1", reaped)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateFailed ||
		updated.StateReasonCode != "runtime_lock_stale" ||
		updated.ExecutionGrantedAt != nil {
		t.Fatalf("unaccepted process after stale runtime reap = %+v, want failed before grant", updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"runtime_lock_stale",
	)
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("check failed process accept: %v", err)
	} else if found {
		t.Fatal("failed unaccepted process remained acceptable")
	}
}

func TestExpiredAgentRuntimeLockRetainsAcceptedProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "stale_runtime_preserves_process")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "stale_runtime_preserves_process", "run_command")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}

	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, fixture.Lock.ID)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap stale runtime locks: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped stale locks = %d, want 1", reaped)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateStarting ||
		updated.StateReasonCode != "" ||
		updated.ExecutionGrantedAt == nil {
		t.Fatalf("process after stale runtime reap = %+v, want granted starting process", updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
		t.Fatalf("tool call after stale runtime reap = %+v, want waiting", toolCall)
	}

	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(2*time.Second))
	updated, err = fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get started process: %v", err)
	}
	if updated.State != executionstore.ProcessStateRunning {
		t.Fatalf("process after daemon start = %+v, want running", updated)
	}
	toolCall, err = fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get completed process tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, "")
}

func TestExpiredAgentRuntimeLockRetainsAcceptedProcessAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "stale_runtime_preserves_process_actions")
	startToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"stale_runtime_preserves_process_actions_start",
		"run_command",
	)

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    startToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
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
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release initial runtime lock: %v", err)
	}
	actionRuntime, err := fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire action runtime lock: %v", err)
	}
	fixture.Lock = actionRuntime
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"stale_runtime_preserves_process_actions",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("stale_runtime_preserves_accepted_process_action", "read_process"),
			builtInProcessToolCallBatchItem("stale_runtime_preserves_queued_process_action", "read_process"),
		},
	)
	acceptedToolCallID, queuedToolCallID := toolCallIDs[0], toolCallIDs[1]
	acceptedAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    acceptedToolCallID,
		RuntimeLockID: actionRuntime.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create accepted process action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		acceptedAction.ID); err != nil {
		t.Fatalf("accept process action: %v", err)
	} else if !found {
		t.Fatal("expected process action accept")
	}
	queuedAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    queuedToolCallID,
		RuntimeLockID: actionRuntime.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":1}`),
	})
	if err != nil {
		t.Fatalf("create queued process action: %v", err)
	}

	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, actionRuntime.ID)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap stale runtime locks: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped stale locks = %d, want 1", reaped)
	}
	acceptedUpdated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedToolCallID,
	)
	if err != nil {
		t.Fatalf("get accepted process action: %v", err)
	}
	if !found || acceptedUpdated.ID != acceptedAction.ID ||
		acceptedUpdated.State != executionstore.ProcessActionStateAccepted ||
		acceptedUpdated.StateReasonCode != "" {
		t.Fatalf("accepted process action after stale runtime reap found=%v action=%+v", found, acceptedUpdated)
	}
	acceptedToolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedToolCallID,
	)
	if err != nil {
		t.Fatalf("get accepted action tool call: %v", err)
	}
	if acceptedToolCall.State != executionstore.ToolCallStateWaiting || acceptedToolCall.CompletedAt != nil {
		t.Fatalf("accepted action tool call after stale runtime reap = %+v, want waiting", acceptedToolCall)
	}
	queuedUpdated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		queuedToolCallID,
	)
	if err != nil {
		t.Fatalf("get queued process action: %v", err)
	}
	if !found || queuedUpdated.ID != queuedAction.ID ||
		queuedUpdated.State != executionstore.ProcessActionStateFailed ||
		queuedUpdated.StateReasonCode != "runtime_lock_stale" {
		t.Fatalf("queued process action after stale runtime reap found=%v action=%+v", found, queuedUpdated)
	}
	queuedToolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		queuedToolCallID,
	)
	if err != nil {
		t.Fatalf("get queued action tool call: %v", err)
	}
	queuedResult := completedToolCallForTest(
		t,
		fixture.Store,
		fixture.AgentID,
		queuedToolCall.TurnID,
		queuedToolCall.ID,
	)
	var queuedParts []struct {
		Type  string `json:"type"`
		Value struct {
			State     executionstore.ProcessState `json:"state"`
			ErrorCode string                      `json:"error_code"`
			Done      bool                        `json:"done"`
		} `json:"value"`
	}
	if err := json.Unmarshal(
		queuedResult.ResultContentParts,
		&queuedParts,
	); err != nil {
		t.Fatalf("decode failed queued read result: %v", err)
	}
	if len(queuedParts) != 1 ||
		queuedParts[0].Type != "structured_data" ||
		queuedParts[0].Value.State != executionstore.ProcessStateRunning ||
		queuedParts[0].Value.ErrorCode != "runtime_lock_stale" ||
		queuedParts[0].Value.Done {
		t.Fatalf(
			"failed queued read result = %s",
			queuedResult.ResultContentParts,
		)
	}

	applied, err := fixture.Store.Execution().ApplyDaemonProcessAction(ctx, executionstore.CompleteDaemonProcessActionInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ProcessID: process.ID,
		ID:        acceptedAction.ID,
		Authority: fixture.authority(),
		Result: json.RawMessage(
			`{"process_id":"` +
				publicResourceID(publicid.KindProcess, process.ID) +
				`","output":"","cursor":0,"next_cursor":0,"truncated":false}`,
		),
	})
	if err != nil || !applied.ToolResultCommitted ||
		applied.Action.State != executionstore.ProcessActionStateApplied {
		t.Fatalf("apply accepted process action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		queuedAction.ID); err != nil {
		t.Fatalf("check failed queued process action accept: %v", err)
	} else if found {
		t.Fatal("failed queued process action remained acceptable")
	}
	acceptedToolCall, err = fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedToolCallID,
	)
	if err != nil {
		t.Fatalf("get completed accepted action tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, acceptedToolCall, "")
}
