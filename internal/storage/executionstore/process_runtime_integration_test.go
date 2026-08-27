//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

type countingSecretKeyWrapper struct {
	secrets.KeyWrapper
	unwrapCalls int
}

func (w *countingSecretKeyWrapper) UnwrapDataKey(
	ctx context.Context,
	wrapped secrets.WrappedDataKey,
	associatedData []byte,
) ([]byte, error) {
	w.unwrapCalls++
	return w.KeyWrapper.UnwrapDataKey(ctx, wrapped, associatedData)
}

func TestDaemonTerminalReportAllowsRegisteredOfflineRuntimeAndReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "report_registered_offline_runtime")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"report_registered_offline_runtime_start",
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
	expireDaemonRuntimeForTest(t, ctx, fixture)
	exitCode := 0
	input := executionstore.CompleteDaemonProcessInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		State:           executionstore.ProcessStateExited,
		ExitCode:        &exitCode,
		Result:          json.RawMessage(`{}`),
		SourceStartedAt: fixture.Now.Add(time.Second),
		SourceEndedAt:   fixture.Now.Add(2 * time.Second),
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input); err != nil {
		t.Fatalf("complete daemon process from registered offline runtime: %v", err)
	}
	if _, err := fixture.Store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority:        fixture.authority(),
			DaemonInstanceID: fixture.DaemonID,
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("restore runtime freshness: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input); err != nil {
		t.Fatalf("complete daemon process after restoring runtime freshness: %v", err)
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	replay, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil {
		t.Fatalf("duplicate terminal report should replay even after runtime is stale: %v", err)
	}
	if replay.Process.ID != process.ID || replay.Process.State != executionstore.ProcessStateExited {
		t.Fatalf("replayed process = %+v, want terminal process %s", replay, process.ID)
	}
}

func TestDaemonProcessStartedReportAllowsRegisteredOfflineRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "started_registered_offline_runtime")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"started_registered_offline_runtime_start",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 1",
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
	expireDaemonRuntimeForTest(t, ctx, fixture)
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
		t.Fatalf("process started report from registered offline runtime: %v", err)
	}
}

func TestAcceptDaemonProcessRechecksLeaseAfterProcessLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "accept_process_post_lock_lease")
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"accept_process_post_lock_lease",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 1",
		ShellSelector:         "sh",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}

	blockingTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin process accept blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get process accept blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(ctx, `
SELECT id
FROM processes
WHERE org_id = $1 AND machine_id = $2 AND id = $3
FOR UPDATE
`, fixture.OrgID, fixture.MachineID, process.ID); err != nil {
		t.Fatalf("lock process before daemon accept: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE daemon_runtimes
SET last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '250 milliseconds',
    updated_at = statement_timestamp()
WHERE org_id = $1 AND machine_id = $2 AND id = $3
`, fixture.OrgID, fixture.MachineID, fixture.RuntimeID); err != nil {
		t.Fatalf("shorten daemon runtime lease: %v", err)
	}

	type acceptResult struct {
		found bool
		err   error
	}
	done := make(chan acceptResult, 1)
	go func() {
		_, found, acceptErr := fixture.Store.Execution().AcceptDaemonProcess(
			context.Background(),
			executionstore.AcceptDaemonProcessInput{Authority: fixture.authority(), ProcessID: process.ID},
		)
		done <- acceptResult{found: found, err: acceptErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, fixture.Store.pool, "-- name: LockDaemonProcessForAccept", blockingPID)
	if _, err := blockingTx.Exec(ctx, `SELECT pg_sleep(0.3)`); err != nil {
		t.Fatalf("wait for daemon runtime lease expiry: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release process accept blocker: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("accept daemon process after lease expiry: %v", result.err)
	}
	if result.found {
		t.Fatal("daemon process was accepted after its runtime lease expired during the process lock wait")
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after rejected accept: %v", err)
	}
	if current.ExecutionGrantedAt != nil || current.State != process.State {
		t.Fatalf("process after rejected accept = %+v, want unchanged ungranted process", current)
	}
}

func TestSupersededDaemonRuntimeCannotReportGrantedProcessAtStorageBoundary(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "superseded_runtime_report")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ToolCallID: createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"superseded_runtime_report_start",
			"run_command",
		),
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 1",
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
	if !found || grant.Process.ExecutionGrantedAt == nil {
		t.Fatalf("accepted process = found %t %+v, want durable execution grant", found, grant)
	}
	replacement, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{
				liveProcessReconciliationClaimForTest(process.ID),
			},
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(3 * time.Second),
		},
	); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("started report from superseded runtime error = %v, want ErrDaemonRuntimeUnregistered", err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			Authority:       fixture.authorityForRuntime(replacement.Runtime.ID),
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			SourceStartedAt: fixture.Now.Add(3 * time.Second),
		},
	); err != nil {
		t.Fatalf("started report from current runtime: %v", err)
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
			SourceEndedAt: fixture.Now.Add(4 * time.Second),
		},
	); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("terminal report from superseded runtime error = %v, want ErrDaemonRuntimeUnregistered", err)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after rejected reports: %v", err)
	}
	if updated.State != executionstore.ProcessStateRunning ||
		updated.ExecutionGrantedAt == nil ||
		!updated.ExecutionGrantedAt.Equal(*grant.Process.ExecutionGrantedAt) {
		t.Fatalf("process after rejected reports = %+v, want unchanged granted running process", updated)
	}
	expireDaemonRuntimeLeaseForTest(
		t,
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		replacement.Runtime.ID,
	)
	ended, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(ctx, 10)
	if err != nil {
		t.Fatalf("end expired replacement runtime: %v", err)
	}
	if len(ended) != 1 || ended[0].ID != replacement.Runtime.ID {
		t.Fatalf("ended replacement runtimes = %+v, want %s", ended, replacement.Runtime.ID)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			Authority:     fixture.authority(),
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			SourceEndedAt: fixture.Now.Add(4 * time.Second),
		},
	); !errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		t.Fatalf("superseded runtime after replacement expiry error = %v, want ErrDaemonRuntimeUnregistered", err)
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	); !errors.Is(err, storeerr.ErrDaemonInstanceSuperseded) {
		t.Fatalf("superseded daemon instance registration error = %v, want ErrDaemonInstanceSuperseded", err)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			Authority:     fixture.authorityForRuntime(replacement.Runtime.ID),
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			SourceEndedAt: fixture.Now.Add(4 * time.Second),
		},
	); err != nil {
		t.Fatalf("terminal report from current expired runtime: %v", err)
	}
}

func TestStartProcessSnapshotsExecutionConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "shell_command_intent")
	secretUser := mustCreateProjectDeveloperUser(
		t,
		ctx,
		fixture.Store,
		"shell-command-env@example.com",
		"Shell Command Env")

	secret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "shell-command-env",
		Material:       secrets.GenericMaterial{Value: "secret-value"},
		Actor:          userPrincipal(secretUser.ID),
	})
	if err != nil {
		t.Fatalf("create process environment secret: %v", err)
	}
	secretID, err := publicid.Encode(publicid.KindSecret, secret.ID)
	if err != nil {
		t.Fatalf("encode process environment secret: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
		UPDATE agent_machine_bindings
		SET env_overlay = '{"APP_ENV":"test"}'::jsonb,
		    secret_env_overlay = jsonb_build_object('API_TOKEN', $1::text, 'API_TOKEN_COPY', $1::text)
		WHERE id = $2
	`, secretID, fixture.BindingID); err != nil {
		t.Fatalf("set process binding environment: %v", err)
	}
	countingWrapper := &countingSecretKeyWrapper{KeyWrapper: newIntegrationKeyWrapper()}
	fixture.Store = newIntegrationStore(fixture.Store.pool, WithSecretKeyWrapper(countingWrapper))
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "shell_command_intent_process", "run_command")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo ok",
		ShellSelector:         "default",
		Cwd:                   "src",
		InitialWaitMS:         750,
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if process.Command != "echo ok" || process.ShellSelector != "default" ||
		process.InitialWaitMS != 750 {
		t.Fatalf("stored command intent = %q/%q", process.Command, process.ShellSelector)
	}
	offers, err := fixture.Store.Execution().ListDaemonProcessOffers(ctx, executionstore.DaemonWorkInput{
		Authority: fixture.authority(),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list process offers: %v", err)
	}
	if len(offers) != 1 || offers[0].Env["APP_ENV"] != "test" || offers[0].Env["API_TOKEN"] != "secret-value" ||
		offers[0].Env["API_TOKEN_COPY"] != "secret-value" ||
		offers[0].Process.InitialWaitMS != 750 {
		t.Fatalf("process offers = %+v, want resolved binding environment", offers)
	}
	if countingWrapper.unwrapCalls != 1 {
		t.Fatalf("secret payload unwrap calls = %d, want 1", countingWrapper.unwrapCalls)
	}
	configuredStore := fixture.Store
	fixture.Store = NewStore(fixture.Store.pool)
	offers, err = fixture.Store.Execution().ListDaemonProcessOffers(ctx, executionstore.DaemonWorkInput{
		Authority: fixture.authority(),
		Limit:     10,
	})
	fixture.Store = configuredStore
	if err != nil {
		t.Fatalf("list process offers with unavailable secret key wrapper: %v", err)
	}
	if len(offers) != 1 || offers[0].RetryError == nil ||
		!strings.Contains(offers[0].RetryError.Error(), "secret key wrapper is required") {
		t.Fatalf("process offers with unavailable secret key wrapper = %+v", offers)
	}
	queuedProcess, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process after transient environment resolution failure: %v", err)
	}
	if queuedProcess.State != executionstore.ProcessStateQueued {
		t.Fatalf("process state after transient environment resolution failure = %q, want queued", queuedProcess.State)
	}
	nulSecret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "shell-command-nul-env",
		Material:       secrets.GenericMaterial{Value: "invalid\x00value"},
		Actor:          userPrincipal(secretUser.ID),
	})
	if err != nil {
		t.Fatalf("create NUL process environment secret: %v", err)
	}
	nulSecretID, err := publicid.Encode(publicid.KindSecret, nulSecret.ID)
	if err != nil {
		t.Fatalf("encode NUL process environment secret: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
		UPDATE agent_machine_bindings
		SET secret_env_overlay = jsonb_build_object('API_TOKEN', $1::text)
		WHERE id = $2
	`, nulSecretID, fixture.BindingID); err != nil {
		t.Fatalf("set NUL process environment secret: %v", err)
	}
	offers, err = fixture.Store.Execution().ListDaemonProcessOffers(ctx, executionstore.DaemonWorkInput{
		Authority: fixture.authority(),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list process offers after binding environment change: %v", err)
	}
	if len(offers) != 1 || offers[0].Env["API_TOKEN"] != "secret-value" ||
		offers[0].Env["API_TOKEN_COPY"] != "secret-value" {
		t.Fatalf("process offers changed with binding environment: %+v", offers)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
		UPDATE processes
		SET secret_env = jsonb_build_object('API_TOKEN', $1::text)
		WHERE id = $2
	`, nulSecretID, process.ID); err != nil {
		t.Fatalf("set NUL process environment snapshot: %v", err)
	}
	offers, err = fixture.Store.Execution().ListDaemonProcessOffers(ctx, executionstore.DaemonWorkInput{
		Authority: fixture.authority(),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list process offers with NUL environment secret: %v", err)
	}
	if len(offers) != 1 || offers[0].Process.ID != process.ID ||
		offers[0].PreparationError != "process environment could not be resolved" || offers[0].Env != nil {
		t.Fatalf("process offers with NUL environment secret = %+v", offers)
	}
	missingSecretID, err := publicid.Encode(publicid.KindSecret, uuid.New())
	if err != nil {
		t.Fatalf("encode missing process environment secret: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
		UPDATE processes
		SET secret_env = jsonb_build_object('API_TOKEN', $1::text)
		WHERE id = $2
	`, missingSecretID, process.ID); err != nil {
		t.Fatalf("set unavailable process environment secret: %v", err)
	}
	offers, err = fixture.Store.Execution().ListDaemonProcessOffers(ctx, executionstore.DaemonWorkInput{
		Authority: fixture.authority(),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list process offers with unavailable environment secret: %v", err)
	}
	if len(offers) != 1 || offers[0].Process.ID != process.ID ||
		offers[0].PreparationError != "process environment could not be resolved" || offers[0].Env != nil {
		t.Fatalf("process offers with unavailable environment secret = %+v", offers)
	}
	accept, found, err := acceptDaemonProcessForTest(
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
	if accept.Process.Command != "echo ok" || accept.Process.ShellSelector != "default" ||
		accept.Process.InitialWaitMS != 750 {
		t.Fatalf("accept process = %+v", accept.Process)
	}
	if accept.Process.Cwd != "/work/src" {
		t.Fatalf("accept process cwd = %q, want /work/src", accept.Process.Cwd)
	}
}

func TestMachineExecutionDefaultUpdatesApplyOnlyToNewProcesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_execution_default_snapshots")
	binding := getAgentMachineBindingForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.BindingID,
	)
	if _, err := fixture.Store.q.UpdateAttachedAgentMachineBindingConfig(
		ctx,
		dbsqlc.UpdateAttachedAgentMachineBindingConfigParams{
			Description:      binding.Description,
			EnvOverlay:       binding.EnvOverlay,
			SecretEnvOverlay: binding.SecretEnvOverlay,
			ProjectID:        testProjectID,
			AgentID:          fixture.AgentID,
			ID:               fixture.BindingID,
		},
	); err != nil {
		t.Fatalf("clear binding cwd: %v", err)
	}
	initialCwd := "/initial"
	initialEnv := json.RawMessage(`{"BASE":"initial"}`)
	if _, err := fixture.Store.Execution().UpdateMachine(ctx, executionstore.UpdateMachineInput{
		OrgID:     testOrgID,
		MachineID: fixture.MachineID,
		Cwd:       &initialCwd,
		Env:       &initialEnv,
	}); err != nil {
		t.Fatalf("set initial machine defaults: %v", err)
	}
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"machine_execution_default_snapshots",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("machine_defaults_first", "run_command"),
			builtInProcessToolCallBatchItem("machine_defaults_second", "run_command"),
		},
	)
	firstTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallIDs[0],
		RuntimeLockID: fixture.Lock.ID,
	}
	firstInput := executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo first",
		ShellSelector:         "sh",
	}
	first, err := startProcessForTest(ctx, fixture.Store, firstTransaction, firstInput)
	if err != nil {
		t.Fatalf("start first process: %v", err)
	}
	changedCwd := "/changed"
	changedEnv := json.RawMessage(`{"BASE":"changed"}`)
	if _, err := fixture.Store.Execution().UpdateMachine(ctx, executionstore.UpdateMachineInput{
		OrgID:     testOrgID,
		MachineID: fixture.MachineID,
		Cwd:       &changedCwd,
		Env:       &changedEnv,
	}); err != nil {
		t.Fatalf("change machine defaults: %v", err)
	}
	replayed, err := startProcessForTest(ctx, fixture.Store, firstTransaction, firstInput)
	if err != nil {
		t.Fatalf("replay first process after machine defaults change: %v", err)
	}
	if replayed.ID != first.ID || replayed.Cwd != initialCwd {
		t.Fatalf("replayed first process = %+v, want original process %s with cwd %q", replayed, first.ID, initialCwd)
	}
	second, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallIDs[1],
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo second",
		ShellSelector:         "sh",
	})
	if err != nil {
		t.Fatalf("start second process: %v", err)
	}
	var firstCwd, secondCwd string
	var firstEnv, secondEnv json.RawMessage
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT cwd, env FROM processes WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		fixture.AgentID,
		first.ID,
	).Scan(&firstCwd, &firstEnv); err != nil {
		t.Fatalf("load first process snapshot: %v", err)
	}
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT cwd, env FROM processes WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		fixture.AgentID,
		second.ID,
	).Scan(&secondCwd, &secondEnv); err != nil {
		t.Fatalf("load second process snapshot: %v", err)
	}
	if firstCwd != initialCwd || !sameJSON(firstEnv, initialEnv) {
		t.Fatalf("first process snapshot = cwd %q env %s", firstCwd, firstEnv)
	}
	if secondCwd != changedCwd || !sameJSON(secondEnv, changedEnv) {
		t.Fatalf("second process snapshot = cwd %q env %s", secondCwd, secondEnv)
	}
}

func TestStartProcessRejectsOversizedLiteralEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "oversized_literal_environment")
	envOverlay, err := json.Marshal(map[string]string{
		"VALUE": strings.Repeat("x", executionstore.MaxResolvedEnvironmentBytes),
	})
	if err != nil {
		t.Fatalf("marshal oversized environment: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
		UPDATE agent_machine_bindings
		SET env_overlay = $1::jsonb
		WHERE id = $2
	`, envOverlay, fixture.BindingID); err != nil {
		t.Fatalf("set oversized binding environment: %v", err)
	}
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"oversized_literal_environment_process",
		"run_command",
	)
	if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo ok",
		ShellSelector:         "default",
	}); err == nil || !strings.Contains(err.Error(), "process environment exceeds size limit") {
		t.Fatalf("start process error = %v, want environment size rejection", err)
	}
	var processCount int
	if err := fixture.Store.pool.QueryRow(
		ctx,
		"SELECT count(*) FROM processes WHERE tool_call_id = $1",
		toolCallID,
	).Scan(&processCount); err != nil {
		t.Fatalf("count oversized environment processes: %v", err)
	}
	if processCount != 0 {
		t.Fatalf("oversized environment process count = %d, want 0", processCount)
	}
}

func TestDaemonProcessAcceptGrantsQueuedProcessOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_accept")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"process_accept",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("process_accept_start", "run_command"),
			builtInProcessToolCallBatchItem("process_accept_queued_after_grant", "run_command"),
		},
	)
	toolCallID, queuedToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 1",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if process.State != executionstore.ProcessStateQueued ||
		process.ExecutionGrantedAt != nil {
		t.Fatalf("new process = %+v, want queued without an execution grant", process)
	}
	accept, found, err := acceptDaemonProcessForTest(
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
		t.Fatal("expected queued process to accept")
	}
	if accept.Process.ID != process.ID ||
		accept.Process.State != executionstore.ProcessStateStarting ||
		accept.Process.ExecutionGrantedAt == nil ||
		!accept.Process.StateChangedAt.Equal(*accept.Process.ExecutionGrantedAt) ||
		!accept.Process.UpdatedAt.Equal(*accept.Process.ExecutionGrantedAt) {
		t.Fatalf("unexpected accept: %+v", accept)
	}
	queued, err := startProcessForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    queuedToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "echo queued",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("start later queued process: %v", err)
	}
	offers, err := fixture.Store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authority(),
			Limit:     1,
		},
	)
	if err != nil {
		t.Fatalf("list process offers after an earlier grant: %v", err)
	}
	if len(offers) != 1 || offers[0].Process.ID != queued.ID {
		t.Fatalf(
			"offers after an earlier grant = %+v, want queued process %s",
			offers,
			queued.ID,
		)
	}
	_, found, err = acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID)

	if err != nil {
		t.Fatalf("repeat process accept: %v", err)
	}
	if found {
		t.Fatal("accepted process should not accept a second time")
	}
	started, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	)
	if err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	if !started.ToolResultCommitted {
		t.Fatalf("first started report was not committed: %+v", started)
	}
	startedReplay, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("replay process started: %v", err)
	}
	if !startedReplay.ToolResultCommitted ||
		started.Process.SourceStartedAt == nil ||
		startedReplay.Process.SourceStartedAt == nil ||
		startedReplay.Process.ID != started.Process.ID ||
		startedReplay.Process.State != executionstore.ProcessStateRunning ||
		!startedReplay.Process.SourceStartedAt.Equal(*started.Process.SourceStartedAt) {
		t.Fatalf(
			"started replay = %+v, want same running process started at %v",
			startedReplay,
			started.Process.SourceStartedAt,
		)
	}
	if _, found, err = acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("repeat process accept after start: %v", err)
	} else if found {
		t.Fatal("process accept should be claimed exactly once")
	}
	if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo conflict",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting process start should return ErrIdempotencyConflict, got %v", err)
	}
}

func TestToolCompletionAuthoritiesStayTypeScoped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("process completion only touches built-in calls", func(t *testing.T) {
		fixture := newProcessDaemonFixture(t, ctx, "running_tool_authority_process")
		toolCallIDs := createToolCallBatchForProcessTest(
			t,
			ctx,
			fixture,
			"running_tool_authority_process",
			[]processToolCallBatchItem{
				customProcessToolCallBatchItem("running_scope_custom_process", "lookup_customer"),
				builtInProcessToolCallBatchItem("running_scope_builtin_process", "run_command"),
			},
		)
		customProcessID, builtInProcessID := toolCallIDs[0], toolCallIDs[1]
		process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    builtInProcessID,
			RuntimeLockID: fixture.Lock.ID,
		}, executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "echo done",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		})
		if err != nil {
			t.Fatalf("start built-in process: %v", err)
		}
		if _, found, err := acceptDaemonProcessForTest(
			ctx,
			fixture.Store,
			testOrgID,
			fixture.MachineID,
			fixture.RuntimeID,
			NilID); err != nil {
			t.Fatalf("accept built-in process: %v", err)
		} else if !found {
			t.Fatal("expected built-in process accept")
		}
		exitCode := 0
		if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			State:           executionstore.ProcessStateExited,
			ExitCode:        &exitCode,
			Result:          json.RawMessage(`{"output":"done\n","cursor":0,"next_cursor":5,"truncated":false}`),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
			SourceEndedAt:   fixture.Now.Add(3 * time.Second),
		}); err != nil {
			t.Fatalf("complete built-in process: %v", err)
		}
		assertToolCallStateForTest(t, ctx, fixture, builtInProcessID, "built_in", "completed")
		assertToolCallStateForTest(t, ctx, fixture, customProcessID, "custom", "ready")
	})

	t.Run("process completion rejects a linked custom call", func(t *testing.T) {
		fixture := newProcessDaemonFixture(t, ctx, "running_tool_authority_mislinked")
		toolCallIDs := createToolCallBatchForProcessTest(
			t,
			ctx,
			fixture,
			"running_tool_authority_mislinked",
			[]processToolCallBatchItem{
				builtInProcessToolCallBatchItem("running_scope_mislinked_builtin", "run_command"),
				customProcessToolCallBatchItem("running_scope_mislinked_custom", "lookup_customer"),
			},
		)
		mislinkedBuiltInID, mislinkedCustomID := toolCallIDs[0], toolCallIDs[1]
		mislinkedProcess, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    mislinkedBuiltInID,
			RuntimeLockID: fixture.Lock.ID,
		}, executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "echo wrong",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		})
		if err != nil {
			t.Fatalf("start mislinked process: %v", err)
		}
		if _, found, err := acceptDaemonProcessForTest(
			ctx,
			fixture.Store,
			testOrgID,
			fixture.MachineID,
			fixture.RuntimeID,
			NilID); err != nil {
			t.Fatalf("accept mislinked process: %v", err)
		} else if !found {
			t.Fatal("expected mislinked process accept")
		}
		if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes
SET tool_call_id = $1
WHERE project_id = $2
  AND agent_id = $3
  AND id = $4
	`, mislinkedCustomID, testProjectID, fixture.AgentID, mislinkedProcess.ID); err != nil {
			t.Fatalf("mislink process to custom tool call: %v", err)
		}
		exitCode := 0
		if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              mislinkedProcess.ID,
			Authority:       fixture.authority(),
			State:           executionstore.ProcessStateExited,
			ExitCode:        &exitCode,
			Result:          json.RawMessage(`{"output":"wrong\n","cursor":0,"next_cursor":6,"truncated":false}`),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
			SourceEndedAt:   fixture.Now.Add(3 * time.Second),
		}); err == nil {
			t.Fatal("mislinked process completed custom tool call; want type guard rejection")
		}
		assertToolCallStateForTest(t, ctx, fixture, mislinkedCustomID, "custom", "ready")
	})
}

func TestRunCommandStartCompletesLinkedToolCallWhenAddressable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "run_command_start_complete")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "run_command_start_complete", "run_command")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 1",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	accept, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID)

	if err != nil {
		t.Fatalf("accept process: %v", err)
	}
	if !found || accept.Process.ID != process.ID {
		t.Fatalf("unexpected accept found=%v accept=%+v process=%s", found, accept, process.ID)
	}
	started, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
		},
	)
	if err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	if started.Process.State != executionstore.ProcessStateRunning {
		t.Fatalf("started state = %s, want running", started.Process.State)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State != "completed" || toolCall.CompletedAt == nil {
		t.Fatalf("tool call was not completed by started process: %+v", toolCall)
	}
	publicProcessID, err := publicid.Encode(publicid.KindProcess, process.ID)
	if err != nil {
		t.Fatalf("encode process id: %v", err)
	}
	completed := completedToolCallForTest(t, fixture.Store, fixture.AgentID, toolCall.TurnID, toolCall.ID)
	typedBody := string(completed.ResultContentParts)
	if !strings.Contains(typedBody, publicProcessID) || !strings.Contains(typedBody, `"state": "running"`) {
		t.Fatalf("typed started tool result missing process handle/state: %s", typedBody)
	}
	var wakeupBefore []byte
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT wake.metadata
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, testProjectID, fixture.AgentID).Scan(&wakeupBefore); err != nil {
		t.Fatalf("load started-process wakeup: %v", err)
	}
	exitCode := 0
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            process.ID,
		Authority:     fixture.authority(),
		State:         executionstore.ProcessStateExited,
		ExitCode:      &exitCode,
		Result:        json.RawMessage(`{"state":"exited","output":"done\n","cursor":0,"next_cursor":5,"truncated":false,"done":true}`),
		SourceEndedAt: fixture.Now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("complete already-addressable process: %v", err)
	}
	var wakeupAfter []byte
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT wake.metadata
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, testProjectID, fixture.AgentID).Scan(&wakeupAfter); err != nil {
		t.Fatalf("load terminal-report wakeup: %v", err)
	}
	if !sameJSON(wakeupBefore, wakeupAfter) {
		t.Fatalf(
			"benign terminal report changed wakeup metadata before=%s after=%s",
			string(wakeupBefore),
			string(wakeupAfter),
		)
	}
}

func TestUploadArtifactCompletesLinkedToolCallWithoutParsingTerminalOutput(t *testing.T) {
	for _, test := range []struct {
		name           string
		createArtifact bool
		wantOutcome    executionstore.ToolResultOutcome
	}{
		{
			name:           "stored_artifact",
			createArtifact: true,
			wantOutcome:    executionstore.ToolResultOutcomeSucceeded,
		},
		{
			name:        "missing_artifact",
			wantOutcome: executionstore.ToolResultOutcomeFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProcessDaemonFixture(t, ctx, "upload_artifact_terminal_"+test.name)
			toolCallID := createToolCallForProcessTest(
				t,
				ctx,
				fixture,
				"upload_artifact_terminal_"+test.name,
				"upload_artifact",
			)
			process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallID,
				RuntimeLockID: fixture.Lock.ID,
			}, executionstore.CreateProcessInput{
				AgentMachineBindingID: fixture.BindingID,
				Command:               "upload artifact",
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
			); err != nil {
				t.Fatalf("accept process: %v", err)
			} else if !found {
				t.Fatal("expected process accept")
			}
			started, err := fixture.Store.Execution().MarkProcessStarted(
				ctx,
				executionstore.MarkProcessStartedInput{
					ProjectID:       testProjectID,
					AgentID:         fixture.AgentID,
					ID:              process.ID,
					Authority:       fixture.authority(),
					SourceStartedAt: fixture.Now.Add(time.Second),
				},
			)
			if err != nil {
				t.Fatalf("mark process started: %v", err)
			}
			if started.ToolResultCommitted {
				t.Fatal("started upload process completed its linked tool call")
			}
			toolCall, err := fixture.Store.Execution().GetToolCall(
				ctx,
				testProjectID,
				fixture.AgentID,
				toolCallID,
			)
			if err != nil {
				t.Fatalf("get waiting tool call: %v", err)
			}
			if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
				t.Fatalf("tool call after started upload = %+v, want waiting", toolCall)
			}

			artifactID := uuid.New()
			filename := "screenshot.png"
			sizeBytes := int64(2048)
			if test.createArtifact {
				idempotencyKey := executionstore.UploadArtifactIdempotencyKey(toolCallID)
				if _, err := fixture.Store.q.InsertArtifact(ctx, dbsqlc.InsertArtifactParams{
					ID:             artifactID,
					ProjectID:      testProjectID,
					AgentID:        fixture.AgentID,
					ContentType:    "image/png",
					Filename:       &filename,
					SizeBytes:      &sizeBytes,
					IdempotencyKey: &idempotencyKey,
				}); err != nil {
					t.Fatalf("insert uploaded artifact: %v", err)
				}
			}

			exitCode := 0
			completionInput := executionstore.CompleteDaemonProcessInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ID:            process.ID,
				Authority:     fixture.authority(),
				State:         executionstore.ProcessStateExited,
				ExitCode:      &exitCode,
				Result:        json.RawMessage(`{"output":"not json","truncated":true}`),
				SourceEndedAt: fixture.Now.Add(2 * time.Second),
			}
			completed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completionInput)
			if err != nil {
				t.Fatalf("complete upload process: %v", err)
			}
			if !completed.ToolResultCommitted {
				t.Fatal("terminal upload process did not commit its tool result")
			}
			toolCall, err = fixture.Store.Execution().GetToolCall(
				ctx,
				testProjectID,
				fixture.AgentID,
				toolCallID,
			)
			if err != nil {
				t.Fatalf("get completed tool call: %v", err)
			}
			result := completedToolCallForTest(
				t,
				fixture.Store,
				fixture.AgentID,
				toolCall.TurnID,
				toolCall.ID,
			)
			wantValue := map[string]any{
				"process_id": publicResourceID(publicid.KindProcess, process.ID),
				"state":      executionstore.ProcessStateExited,
				"error":      "upload completed without an artifact",
			}
			if test.createArtifact {
				wantValue = map[string]any{
					"process_id":   publicResourceID(publicid.KindProcess, process.ID),
					"artifact_id":  publicResourceID(publicid.KindArtifact, artifactID),
					"filename":     filename,
					"content_type": "image/png",
					"size_bytes":   sizeBytes,
				}
			}
			wantResult, err := json.Marshal([]map[string]any{{
				"type":  "structured_data",
				"value": wantValue,
			}})
			if err != nil {
				t.Fatalf("marshal expected tool result: %v", err)
			}
			if result.Outcome != test.wantOutcome || !sameJSON(result.ResultContentParts, wantResult) {
				t.Fatalf("upload tool result = %+v, want %s", result, wantResult)
			}
			replayed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completionInput)
			if err != nil {
				t.Fatalf("replay upload process completion: %v", err)
			}
			if !replayed.ToolResultCommitted {
				t.Fatal("replayed upload completion did not recognize its durable result")
			}
		})
	}
}

func TestRunCommandTerminalResultRetainsCanonicalProcessHandle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal_command_no_handle")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "terminal_command_no_handle", "run_command")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo done",
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
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ID:        process.ID,
		Authority: fixture.authority(),
		State:     executionstore.ProcessStateExited,
		ExitCode:  &exitCode,
		Result: json.RawMessage(
			`{"process_id":"prc_untrusted","state":"exited","output":"done\n","cursor":0,"next_cursor":5,"truncated":false,"done":true}`,
		),
		SourceStartedAt: fixture.Now.Add(time.Second),
		SourceEndedAt:   fixture.Now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	completed := completedToolCallForTest(t, fixture.Store, fixture.AgentID, toolCall.TurnID, toolCall.ID)
	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
	if !strings.Contains(string(completed.ResultContentParts), publicProcessID) ||
		strings.Contains(string(completed.ResultContentParts), "prc_untrusted") {
		t.Fatalf("terminal run_command result has the wrong process handle: %s", string(completed.ResultContentParts))
	}
}

func TestDaemonProcessQueuedWorkCancelsBeforeAccept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_accept_stopped")

	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"process_accept_stopped_start",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 1",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	queuedToolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get queued run command: %v", err)
	}
	if queuedToolCall.State != "waiting" || queuedToolCall.Outcome != "" {
		t.Fatalf(
			"queued process lacks unresolved waiting tool call: %+v",
			queuedToolCall,
		)
	}
	appendStopEventForProcessTest(t, ctx, fixture)
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID); err != nil {
		t.Fatalf("accept process after stop: %v", err)
	} else if found {
		t.Fatal("queued process remained acceptable after agent cancel")
	}
	updated, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get canceled queued process: %v", err)
	}
	if updated.State != executionstore.ProcessStateFailed ||
		updated.StateReasonCode != "agent_canceled_before_grant" {
		t.Fatalf("canceled queued process = %+v", updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get canceled run command: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"canceled",
	)
}

func TestStartProcessAcceptsPTYIOMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "pty_accepted")
	toolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "pty_accepted_process")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		IOMode:                "pty",
		Command:               "vim",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start pty process: %v", err)
	}
	if process.IOMode != "pty" {
		t.Fatalf("io mode = %q, want pty", process.IOMode)
	}
}

func TestProcessAcceptUsesReplacementGrantAfterGrantRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "grant_rotation")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"grant_rotation",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("grant_rotation_original", "run_command"),
			builtInProcessToolCallBatchItem("grant_rotation_replacement", "run_command"),
		},
	)
	originalToolCallID, replacementToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    originalToolCallID,
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
	if process.AgentMachineBindingID != fixture.BindingID {
		t.Fatalf("process binding = %s, want fixture binding %s", process.AgentMachineBindingID, fixture.BindingID)
	}
	if _, err := fixture.Store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		fixture.GrantID); err != nil {
		t.Fatalf("revoke original grant: %v", err)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get revoked-grant process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed || current.StateReasonCode != "project_machine_grant_revoked" ||
		current.SourceEndedAt != nil {
		t.Fatalf("revoked-grant process = %+v, want failed/project_machine_grant_revoked", current)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, originalToolCallID)
	if err != nil {
		t.Fatalf("get revoked-grant tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, "project_machine_grant_revoked")
	newGrant, _, err := fixture.Store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
		OrgID:     testOrgID,
		ProjectID: testProjectID,
		MachineID: fixture.MachineID,
	})
	if err != nil {
		t.Fatalf("create replacement grant: %v", err)
	}
	if newGrant.ID == fixture.GrantID {
		t.Fatalf("replacement grant reused revoked grant id %s", newGrant.ID)
	}
	replacementProcess, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    replacementToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "pwd",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start replacement-grant process: %v", err)
	}
	if replacementProcess.AgentMachineBindingID != fixture.BindingID {
		t.Fatalf("replacement process binding = %s, want %s", replacementProcess.AgentMachineBindingID, fixture.BindingID)
	}
	accept, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID)

	if err != nil {
		t.Fatalf("accept process after grant rotation: %v", err)
	}
	if !found {
		t.Fatal("expected replacement-grant process to accept")
	}
	if accept.Process.ID != replacementProcess.ID {
		t.Fatalf(
			"accepted process = %s, want replacement process %s; revoked-grant process %s must remain unaccepted",
			accept.Process.ID,
			replacementProcess.ID,
			process.ID,
		)
	}
}

func TestRevokeProjectMachineGrantTerminatesActiveDaemonProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "revoke_grant_active_process")
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, WithPostCommitPublisher(publisher))
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "revoke_grant_active_process", "run_command")
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
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
		process.ID); err != nil ||
		!found {
		t.Fatalf("accept process before grant revoke found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))

	if _, err := fixture.Store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		fixture.GrantID); err != nil {
		t.Fatalf("revoke project machine grant: %v", err)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get revoked-grant process: %v", err)
	}
	if current.State != executionstore.ProcessStateUnknown || current.StateReasonCode != "project_machine_grant_revoked" ||
		current.SourceEndedAt != nil {
		t.Fatalf("revoked-grant process = %+v, want unknown/project_machine_grant_revoked", current)
	}
	if !publisher.hasProcessTermination(fixture.MachineID, process.ID) {
		t.Fatalf("grant revoke did not publish daemon process termination for process %s", process.ID)
	}
}

func TestRevokeProjectMachineGrantResolvesTerminalProcessActions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"revoke_grant_terminal_actions",
	)
	results := createTerminalProcessActionsForLifecycleTest(
		t,
		ctx,
		fixture,
		[]terminalProcessActionTestInput{
			{
				Name:     "revoke_grant_terminal_read",
				ToolName: "read_process",
				Kind:     executionstore.ProcessActionKindRead,
			},
			{
				Name:     "revoke_grant_terminal_write",
				ToolName: "write_process",
				Kind:     executionstore.ProcessActionKindWrite,
				Accepted: true,
			},
		},
	)
	read, readToolCallID := results[0].Action, results[0].ToolCallID
	write, writeToolCallID := results[1].Action, results[1].ToolCallID
	if _, err := fixture.Store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		fixture.GrantID); err != nil {
		t.Fatalf("revoke project machine grant: %v", err)
	}
	for _, expected := range []struct {
		action     executionstore.ProcessActionRecord
		toolCallID ID
		state      executionstore.ProcessActionState
	}{
		{
			action:     read,
			toolCallID: readToolCallID,
			state:      executionstore.ProcessActionStateFailed,
		},
		{
			action:     write,
			toolCallID: writeToolCallID,
			state:      executionstore.ProcessActionStateUnknown,
		},
	} {
		action, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get terminal process action: %v", err)
		}
		if !found ||
			action.ID != expected.action.ID ||
			action.State != expected.state ||
			action.StateReasonCode != "project_machine_grant_revoked" {
			t.Fatalf(
				"terminal action after grant revoke = found %t %+v",
				found,
				action,
			)
		}
		toolCall, err := fixture.Store.Execution().GetToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get terminal action tool call: %v", err)
		}
		assertCompletedToolCallWithResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			"project_machine_grant_revoked",
		)
	}
}

func TestArchiveAgentMarksActiveProcessUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "archive_active_process")
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, WithPostCommitPublisher(publisher))
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "archive_active_process", "run_command")
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
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
		process.ID); err != nil ||
		!found {
		t.Fatalf("accept process before archive found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	if _, _, err := fixture.Store.Execution().ArchiveAgent(
		ctx,
		testProjectID,
		fixture.AgentID,
		userPrincipal(fixture.UserID)); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get archived-agent process: %v", err)
	}
	if current.State != executionstore.ProcessStateUnknown || current.StateReasonCode != "agent_archived" ||
		current.SourceEndedAt != nil {
		t.Fatalf("archived-agent process = %+v, want unknown/agent_archived", current)
	}
	if !publisher.hasProcessTermination(fixture.MachineID, process.ID) {
		t.Fatalf("archive did not publish daemon process termination for process %s", process.ID)
	}
}

func TestReplacementRuntimeMarksUnclaimedGrantedProcessUnknownAfterRuntimeEnds(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "daemon_runtime_released_missing_local")
	toolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "daemon_runtime_released_missing_local")

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
	if _, err := fixture.Store.Execution().EndDaemonRuntime(
		ctx,
		fixture.authority(),
	); err != nil {
		t.Fatalf("end daemon runtime: %v", err)
	}
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID(t.Name() + "-replacement"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	if len(registration.Reconciliation.Processes) != 0 {
		t.Fatalf(
			"empty local process state should need no local dispositions, got %+v",
			registration.Reconciliation,
		)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateUnknown ||
		updated.StateReasonCode != executionstore.LocalProcessMissingAfterDaemonReconnectReason ||
		updated.ExecutionGrantedAt == nil ||
		!updated.ExecutionGrantedAt.Equal(*grant.Process.ExecutionGrantedAt) {
		t.Fatalf(
			"process after empty replacement reconciliation = %+v, want unknown %s",
			updated,
			executionstore.LocalProcessMissingAfterDaemonReconnectReason,
		)
	}
}

func assertToolCallStateForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
	wantType, wantState string,
) {
	t.Helper()
	var gotType, gotState string
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT call.type, call.state
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.id = $3
	`, testProjectID, fixture.AgentID, toolCallID).Scan(&gotType, &gotState); err != nil {
		t.Fatalf("load tool call state: %v", err)
	}
	if gotType != wantType || gotState != wantState {
		t.Fatalf("tool call %s type/state = %s/%s, want %s/%s", toolCallID, gotType, gotState, wantType, wantState)
	}
}
