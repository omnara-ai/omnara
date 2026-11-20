package groundbox

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestGroundboxProvisionCreatesOneSandboxAndStartsTheDaemon(t *testing.T) {
	api := &fakeAPI{}
	machineProvider := testProvider(api)
	provisioning := testProvisioning(t, map[string]any{"template": "omnara-box", "startup_script": "echo ready"})

	result, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID, provisioning, "machine-token",
		map[string]string{"FOO": "bar", "bad-name": "x"}, true,
	)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.ProviderResourceID != "sbx_test0001" || result.SandboxURL != "" {
		t.Fatalf("result = %+v, want the created sandbox and no sandbox url without sleep", result)
	}
	if len(api.creates) != 1 {
		t.Fatalf("creates = %d, want 1", len(api.creates))
	}
	name, err := providers.MachineAllocationName(testInstallationID, testMachineID)
	if err != nil {
		t.Fatal(err)
	}
	create := api.creates[0]
	if api.createKeys[0] != name || create.CPUs != 2 || create.MemoryGB != 4 || create.Template != "omnara-box" ||
		create.TimeoutMinutes != sandboxTimeoutMinutes || create.IdleTimeoutSeconds != 0 {
		t.Fatalf("create = %+v key %q, want the machine size, template, and allocation key", create, api.createKeys[0])
	}
	if !ownedBy(sandbox{Metadata: create.Metadata}, testInstallationID, testMachineID) {
		t.Fatalf("create metadata = %v, want Omnara ownership", create.Metadata)
	}
	if len(api.commandReqs) != 1 {
		t.Fatalf("commands = %d, want one daemon keeper", len(api.commandReqs))
	}
	keeper := api.commandReqs[0]
	if keeper.Cmd != bootCommand || keeper.TimeoutS != keeperTimeoutSeconds {
		t.Fatalf("keeper = %q timeout %d, want the boot command", keeper.Cmd, keeper.TimeoutS)
	}
	if keeper.Env[providers.MachineTokenEnvVar] != "machine-token" || keeper.Env["FOO"] != "bar" ||
		keeper.Env[providers.ManagedBootstrapScriptEnvVar] == "" {
		t.Fatalf("keeper env = %v, want the token, machine env, and bootstrap", keeper.Env)
	}
	if _, ok := keeper.Env["bad-name"]; ok {
		t.Fatal("keeper env kept a variable name the shell cannot export")
	}
	if strings.Contains(keeper.Cmd, "machine-token") {
		t.Fatal("the machine token must travel in the command env, not its listed command line")
	}
	if _, ok := keeper.Env[daemonprotocol.SleepAfterEnvVar]; ok {
		t.Fatal("keeper env enables sleep without sleep_after_ms")
	}
}

func TestGroundboxProvisionWithSleepSetsIdlePauseAndWakeEnv(t *testing.T) {
	api := &fakeAPI{}
	result, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID,
		testProvisioning(t, map[string]any{"sleep_after_ms": 45000}), "machine-token", nil, true,
	)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.SandboxURL != "https://api.groundbox.dev/v1/sandboxes/sbx_test0001" {
		t.Fatalf("sandbox url = %q, want the sandbox's API URL so the machine can sleep", result.SandboxURL)
	}
	if api.creates[0].IdleTimeoutSeconds != idleTimeoutSeconds {
		t.Fatalf("idle timeout = %d, want %d", api.creates[0].IdleTimeoutSeconds, idleTimeoutSeconds)
	}
	env := api.commandReqs[0].Env
	if env[daemonprotocol.SleepAfterEnvVar] != "45000" ||
		env[daemonprotocol.SleepPlatformEnvVar] != daemonprotocol.SleepPlatformGroundbox ||
		env[daemonprotocol.WakeListenAddrEnvVar] != "127.0.0.1:8377" {
		t.Fatalf("keeper env = %v, want the groundbox sleep settings", env)
	}
}

func TestGroundboxProvisionRetryAdoptsTheSandboxAndKeepsOneDaemon(t *testing.T) {
	api := &fakeAPI{}
	machineProvider := testProvider(api)
	provisioning := testProvisioning(t, nil)
	first, err := machineProvider.ProvisionMachine(
		context.Background(), testInstallationID, testMachineID, provisioning, "token", nil, true,
	)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	for _, firstAttempt := range []bool{true, false} {
		again, err := machineProvider.ProvisionMachine(
			context.Background(), testInstallationID, testMachineID, provisioning, "token", nil, firstAttempt,
		)
		if err != nil {
			t.Fatalf("retry (first attempt %t): %v", firstAttempt, err)
		}
		if again.ProviderResourceID != first.ProviderResourceID {
			t.Fatalf("retry returned %q, want %q", again.ProviderResourceID, first.ProviderResourceID)
		}
	}
	if len(api.creates) != 1 || len(api.commandReqs) != 1 {
		t.Fatalf("creates = %d commands = %d, want one of each", len(api.creates), len(api.commandReqs))
	}
}

func TestGroundboxProvisionWithoutAFirstAttemptNeverCreates(t *testing.T) {
	api := &fakeAPI{}
	_, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID, testProvisioning(t, nil), "token", nil, false,
	)
	if err == nil || !strings.Contains(err.Error(), "create outcome is unknown") {
		t.Fatalf("provision = %v, want the unknown create outcome", err)
	}
	if len(api.creates) != 0 {
		t.Fatal("a later attempt must not create a second sandbox")
	}
}

func TestGroundboxProvisionClassifiesCreateErrors(t *testing.T) {
	for name, test := range map[string]struct {
		err       error
		permanent bool
		retried   bool
	}{
		"rejected":     {err: statusError(http.StatusForbidden, "trial_limit"), permanent: true},
		"no credit":    {err: statusError(http.StatusPaymentRequired, "out_of_credit"), permanent: true},
		"rate limited": {err: statusError(http.StatusTooManyRequests, "rate_limited"), retried: true},
		"conflict":     {err: statusError(http.StatusConflict, "conflict")},
		"server error": {err: statusError(http.StatusBadGateway, "")},
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeAPI{createErr: test.err}
			machineProvider := testProvider(api)
			_, err := machineProvider.ProvisionMachine(
				context.Background(), testInstallationID, testMachineID, testProvisioning(t, nil), "token", nil, true,
			)
			if err == nil || errors.Is(err, providers.ErrPermanent) != test.permanent {
				t.Fatalf("provision = %v, want permanent %t", err, test.permanent)
			}
			if machineProvider.createAttempted == test.retried {
				t.Fatalf("create attempted = %t, want a retry to create again: %t", machineProvider.createAttempted, test.retried)
			}
		})
	}
}

func TestGroundboxProvisionResumesAPausedSandbox(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_paused01", sandboxStatusPaused)
	result, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID, testProvisioning(t, nil), "token", nil, false,
	)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.ProviderResourceID != "sbx_paused01" || len(api.resumes) != 1 {
		t.Fatalf("result = %+v resumes = %v, want the paused sandbox resumed", result, api.resumes)
	}
}

func TestGroundboxProvisionStartsAShutDownSandbox(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_stopped1", sandboxStatusShutDown)
	if _, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID, testProvisioning(t, nil), "token", nil, false,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(api.starts) != 1 || len(api.resumes) != 0 {
		t.Fatalf("starts = %v resumes = %v, want a start", api.starts, api.resumes)
	}
}

func TestGroundboxProvisionFailsPermanentlyOnAFailedSandbox(t *testing.T) {
	api := &fakeAPI{createState: sandboxStatusFailed}
	result, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID, testProvisioning(t, nil), "token", nil, true,
	)
	if !errors.Is(err, providers.ErrPermanent) || result.ProviderResourceID != "sbx_test0001" {
		t.Fatalf("provision = %+v, %v; want a permanent error with the observed id", result, err)
	}
}

func TestGroundboxProvisionRejectsAResizedSandbox(t *testing.T) {
	api := &fakeAPI{}
	current := ownedSandbox(t, api, "sbx_small001", sandboxStatusRunning)
	current.MemoryGB = 2
	api.sandboxes[current.ID] = current
	_, err := testProvider(api).ProvisionMachine(
		context.Background(), testInstallationID, testMachineID, testProvisioning(t, nil), "token", nil, false,
	)
	if !errors.Is(err, providers.ErrPermanent) || !strings.Contains(err.Error(), "2 GB of memory") {
		t.Fatalf("provision = %v, want a permanent size mismatch", err)
	}
}

func TestGroundboxValidateMachineConfigBoundsTheCommandRequest(t *testing.T) {
	machineProvider := testProvider(&fakeAPI{})
	provisioning := testProvisioning(t, nil)
	if err := machineProvider.ValidateMachineConfig(provisioning, map[string]string{"FOO": "bar"}); err != nil {
		t.Fatalf("validate small env: %v", err)
	}
	large := map[string]string{"BIG": strings.Repeat("x", maxCommandRequestBytes)}
	if err := machineProvider.ValidateMachineConfig(provisioning, large); err == nil {
		t.Fatal("validate accepted an environment larger than a command request")
	}
	if err := machineProvider.ValidateMachineConfig(provisioning, map[string]string{"OMNARA_X": "1"}); err == nil {
		t.Fatal("validate accepted a reserved OMNARA_ variable")
	}
}

func TestGroundboxWakeMachineResumesAndStartsTheWakeKeeper(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_asleep01", sandboxStatusPaused)
	if err := testProvider(api).WakeMachine(context.Background(), providers.WakeMachineInput{
		ProviderResourceID: "sbx_asleep01",
	}); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if len(api.resumes) != 1 || len(api.commandReqs) != 1 || api.commandReqs[0].Cmd != wakeCommand {
		t.Fatalf("resumes = %v commands = %+v, want a resume and the wake keeper", api.resumes, api.commandReqs)
	}
	if len(api.commandReqs[0].Env) != 0 {
		t.Fatalf("wake keeper env = %v, want none", api.commandReqs[0].Env)
	}
}

func TestGroundboxWakeMachineOnARunningSandboxOnlyPokesTheDaemon(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_awake001", sandboxStatusRunning)
	if err := testProvider(api).WakeMachine(context.Background(), providers.WakeMachineInput{
		ProviderResourceID: "sbx_awake001",
	}); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if len(api.resumes) != 0 || len(api.commandReqs) != 1 {
		t.Fatalf("resumes = %v commands = %d, want only the wake keeper", api.resumes, len(api.commandReqs))
	}
}

func TestGroundboxWakeMachineFailures(t *testing.T) {
	if err := testProvider(&fakeAPI{}).WakeMachine(context.Background(), providers.WakeMachineInput{}); err == nil {
		t.Fatal("wake without a resource id must fail")
	}
	if err := testProvider(&fakeAPI{}).WakeMachine(context.Background(), providers.WakeMachineInput{
		ProviderResourceID: "sbx_missing01",
	}); err == nil {
		t.Fatal("wake of a missing sandbox must fail")
	}
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_deleted1", sandboxStatusStopped)
	if err := testProvider(api).WakeMachine(context.Background(), providers.WakeMachineInput{
		ProviderResourceID: "sbx_deleted1",
	}); err == nil || len(api.commandReqs) != 0 {
		t.Fatalf("wake of a stopped sandbox = %v, want an error and no command", err)
	}
}

func TestGroundboxInspectMachine(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_owned001", sandboxStatusPaused)
	api.add("sbx_foreign1", sandboxStatusRunning, 2, 4, map[string]string{machineLabel: "mch_other"})
	machineProvider := testProvider(api)
	provisioning := testProvisioning(t, nil)

	for _, resourceID := range []string{"", "sbx_owned001"} {
		id, found, err := machineProvider.InspectMachine(
			context.Background(), testInstallationID, testMachineID, provisioning, resourceID,
		)
		if err != nil || !found || id != "sbx_owned001" {
			t.Fatalf("inspect %q = %q %t %v", resourceID, id, found, err)
		}
	}
	if _, _, err := machineProvider.InspectMachine(
		context.Background(), testInstallationID, testMachineID, provisioning, "sbx_foreign1",
	); err == nil {
		t.Fatal("inspect accepted a sandbox owned by another machine")
	}
	if _, found, err := machineProvider.InspectMachine(
		context.Background(), testInstallationID, uuid.New(), provisioning, "",
	); err != nil || found {
		t.Fatalf("inspect of an unknown machine = %t %v", found, err)
	}
	api.setStatus("sbx_owned001", sandboxStatusStopped)
	if _, found, err := machineProvider.InspectMachine(
		context.Background(), testInstallationID, testMachineID, provisioning, "sbx_owned001",
	); err != nil || found {
		t.Fatalf("inspect of a stopped sandbox = %t %v, want absent", found, err)
	}
}

func TestGroundboxInspectMachineRejectsDuplicateOwnership(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_owned001", sandboxStatusRunning)
	ownedSandbox(t, api, "sbx_owned002", sandboxStatusRunning)
	_, _, err := testProvider(api).InspectMachine(
		context.Background(), testInstallationID, testMachineID, testProvisioning(t, nil), "",
	)
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("inspect = %v, want a duplicate ownership error", err)
	}
}

func TestGroundboxDeleteMachineIsRetrySafe(t *testing.T) {
	api := &fakeAPI{}
	ownedSandbox(t, api, "sbx_owned001", sandboxStatusRunning)
	machineProvider := testProvider(api)
	provisioning := testProvisioning(t, nil)
	if err := machineProvider.DeleteMachine(
		context.Background(), testInstallationID, testMachineID, provisioning, "",
	); err == nil {
		t.Fatal("delete without a resource id must fail")
	}
	if err := machineProvider.DeleteMachine(
		context.Background(), uuid.New(), testMachineID, provisioning, "sbx_owned001",
	); err == nil {
		t.Fatal("delete accepted another installation's sandbox")
	}
	for range 2 {
		if err := machineProvider.DeleteMachine(
			context.Background(), testInstallationID, testMachineID, provisioning, "sbx_owned001",
		); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if len(api.deleted) != 1 || api.sandboxes["sbx_owned001"].Status != sandboxStatusStopped {
		t.Fatalf("deleted = %v, want one delete", api.deleted)
	}
	if err := machineProvider.DeleteMachine(
		context.Background(), testInstallationID, testMachineID, provisioning, "sbx_gone0001",
	); err != nil {
		t.Fatalf("delete of an absent sandbox: %v", err)
	}
}

func TestGroundboxPrepareProvisioningReturnsTheConfiguredSize(t *testing.T) {
	facts, err := testProvider(&fakeAPI{}).PrepareProvisioning(context.Background(), testProvisioning(t, nil))
	if err != nil || *facts.CPU != 2 || *facts.MemoryMB != 4096 {
		t.Fatalf("facts = %+v, %v", facts, err)
	}
}

func TestGroundboxCheckConnectionListsSandboxes(t *testing.T) {
	if err := testProvider(&fakeAPI{}).CheckConnection(context.Background(), testProvisioning(t, nil)); err != nil {
		t.Fatalf("check connection: %v", err)
	}
	api := &fakeAPI{listErr: statusError(http.StatusUnauthorized, "unauthorized")}
	if err := testProvider(api).CheckConnection(context.Background(), testProvisioning(t, nil)); err == nil {
		t.Fatal("check connection accepted a rejected key")
	}
}
