package boxd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestBoxdProviderProvisionCreatesMachineAndStartsDaemon(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(installationID, machineID)
	require.NoError(t, err)
	result, err := provider.ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "", "echo ready"),
		"machine-token",
		map[string]string{"APP_ENV": "production", "QUOTED": "it's"},
		true,
	)
	if err != nil {
		t.Fatalf("provision boxd machine: %v", err)
	}
	if result.ProviderResourceID != "vm-123" || result.SandboxURL != "" {
		t.Fatalf("result = %+v", result)
	}
	if api.createCalls != 1 || api.createRequest.Name != name || api.createRequest.Snapshot != "" ||
		api.createRequest.VCPU != 2 || api.createRequest.MemoryBytes != 8192*mebibyte {
		t.Fatalf("create request = %+v (calls %d)", api.createRequest, api.createCalls)
	}
	if api.execCalls != 1 || api.execRef != "vm-123" || api.execCommand != launcherCommand() {
		t.Fatalf("exec = calls %d ref %q command %q", api.execCalls, api.execRef, api.execCommand)
	}
	payload := string(api.execStdin)
	for _, want := range []string{
		"export APP_ENV='production'\n",
		`export QUOTED='it'\''s'` + "\n",
		"export OMNARA_MACHINE_TOKEN='machine-token'\n",
		"export OMNARA_API_URL='https://api.omnara.test/v1'\n",
		"export " + testStartupScriptEnvVar + "='",
		`"${OMNARA_INSTALLER_URL:?}"`,
		"start --no-service",
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("boot payload missing %q:\n%s", want, payload)
		}
	}
}

func TestBoxdProviderProvisionEnablesSleepBetweenTurns(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(installationID, machineID)
	require.NoError(t, err)
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testSleepProvisioning(t, 45_000),
		"machine-token",
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("provision sleeping boxd machine: %v", err)
	}
	if result.SandboxURL != "https://"+name+".boxd.sh/" {
		t.Fatalf("sandbox url = %q", result.SandboxURL)
	}
	if api.createRequest.AutoSuspendTimeoutSecs != autoSuspendTimeoutSecs {
		t.Fatalf("auto suspend = %d, want %d", api.createRequest.AutoSuspendTimeoutSecs, autoSuspendTimeoutSecs)
	}
	payload := string(api.execStdin)
	if !strings.Contains(payload, "export OMNARA_DAEMON_SLEEP_AFTER_MS='45000'\n") {
		t.Fatalf("boot payload is missing the sleep window:\n%s", payload)
	}
	if strings.Contains(payload, "OMNARA_DAEMON_SLEEP_PLATFORM") {
		t.Fatalf("boot payload set a sleep platform:\n%s", payload)
	}
	if !strings.Contains(payload, "'\n\n"+bootstrapKeepAwakeScript+"set -eu\n") {
		t.Fatalf("boot payload must stay awake between the env exports and the boot script:\n%s", payload)
	}
}

func TestBoxdProviderProvisionDisablesAutoSuspendWithoutSleep(t *testing.T) {
	api := newFakeAPI()
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err != nil || api.createRequest.AutoSuspendTimeoutSecs != 0 {
		t.Fatalf("auto suspend = %d, error %v, want it off", api.createRequest.AutoSuspendTimeoutSecs, err)
	}
	if strings.Contains(string(api.execStdin), "OMNARA_DAEMON_SLEEP") ||
		strings.Contains(string(api.execStdin), bootstrapKeepAwakeScript) {
		t.Fatalf("boot payload enabled sleep without a sleep window:\n%s", api.execStdin)
	}
}

func TestBoxdProviderProvisionRequiresAnAccessDomain(t *testing.T) {
	api := newFakeAPI()
	api.vm.AccessDomain = ""
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testSleepProvisioning(t, 45_000),
		"machine-token",
		nil,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "missing its access domain") ||
		result.ProviderResourceID != "vm-123" {
		t.Fatalf("missing access domain = result %+v error %v", result, err)
	}
}

func TestBoxdProviderWakeMachinePokesTheDaemon(t *testing.T) {
	api := newFakeAPI()
	api.execResult = execResult{}
	provider := newTestProvider(api)
	if err := provider.WakeMachine(
		context.Background(),
		providers.WakeMachineInput{ProviderResourceID: "vm-123"},
	); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if api.execCalls != 1 || api.execRef != "vm-123" || api.execCommand != wakePokeCommand() ||
		api.execStdin != nil {
		t.Fatalf(
			"wake exec = calls %d ref %q command %q stdin %d bytes",
			api.execCalls,
			api.execRef,
			api.execCommand,
			len(api.execStdin),
		)
	}
	if !strings.Contains(api.execCommand, "127.0.0.1:8377") {
		t.Fatalf("wake poke does not target the daemon wake listener: %q", api.execCommand)
	}
	if err := provider.WakeMachine(
		context.Background(),
		providers.WakeMachineInput{ProviderResourceID: "vm-123"},
	); err != nil || api.execCalls != 2 {
		t.Fatalf("repeat wake = error %v calls %d", err, api.execCalls)
	}
	if err := provider.WakeMachine(context.Background(), providers.WakeMachineInput{}); err == nil ||
		!strings.Contains(err.Error(), "provider resource id") {
		t.Fatalf("wake without a resource id = %v", err)
	}
}

func TestBoxdProviderWakeMachineRetriesWhileTheGuestResumes(t *testing.T) {
	api := newFakeAPI()
	api.execResult = execResult{}
	api.execErrs = []error{
		apiError{Code: codes.Unavailable, Message: "cannot connect to VM agent"},
		apiError{Code: codes.FailedPrecondition, Message: "waking"},
		nil,
	}
	if err := newTestProvider(api).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{ProviderResourceID: "vm-123"},
	); err != nil || api.execCalls != 3 {
		t.Fatalf("wake through resume = error %v calls %d", err, api.execCalls)
	}
	api = newFakeAPI()
	api.execErrs = []error{apiError{Code: codes.PermissionDenied, Message: "VM not accessible"}}
	err := newTestProvider(api).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{ProviderResourceID: "vm-123"},
	)
	if err == nil || !strings.Contains(err.Error(), `wake boxd machine "vm-123"`) || api.execCalls != 1 {
		t.Fatalf("permanent wake failure = error %v calls %d", err, api.execCalls)
	}
}

func TestBoxdProviderWakeMachineReportsAnUnansweredListener(t *testing.T) {
	api := newFakeAPI()
	api.execResult = execResult{ExitCode: 7}
	err := newTestProvider(api).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{ProviderResourceID: "vm-123"},
	)
	if err == nil || !strings.Contains(err.Error(), "wake listener exited with status 7") {
		t.Fatalf("unanswered wake = %v", err)
	}
}

func TestBoxdProviderWakeMachineIsBounded(t *testing.T) {
	api := newFakeAPI()
	if err := newTestProvider(api).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{ProviderResourceID: "vm-123"},
	); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if api.execDeadline.IsZero() || api.execDeadline.After(time.Now().Add(wakeTimeout)) {
		t.Fatalf("wake exec deadline = %v, want within %v", api.execDeadline, wakeTimeout)
	}
}

func TestBoxdProviderProvisionFromSnapshotOmitsSize(t *testing.T) {
	api := newFakeAPI()
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "team-workspace", ""),
		"machine-token",
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("provision boxd machine from snapshot: %v", err)
	}
	if api.createRequest.Snapshot != "team-workspace" || api.createRequest.VCPU != 0 ||
		api.createRequest.MemoryBytes != 0 {
		t.Fatalf("create request = %+v", api.createRequest)
	}
}

func TestBoxdProviderProvisionAdoptsExistingMachine(t *testing.T) {
	api := newFakeAPI()
	api.exists = true
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(installationID, machineID)
	require.NoError(t, err)
	api.vm.Name = name
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err != nil || result.ProviderResourceID != "vm-123" || api.createCalls != 0 || api.execCalls != 1 {
		t.Fatalf(
			"adopt existing = result %+v error %v creates %d execs %d",
			result,
			err,
			api.createCalls,
			api.execCalls,
		)
	}
}

func TestBoxdProviderProvisionConvergesAfterCreateConflict(t *testing.T) {
	api := newFakeAPI()
	api.createErr = apiError{Code: codes.FailedPrecondition, Message: "name is already taken"}
	api.existsAfterCreateErr = true
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err != nil || result.ProviderResourceID != "vm-123" || api.execCalls != 1 {
		t.Fatalf("converge after conflict = result %+v error %v execs %d", result, err, api.execCalls)
	}
	api = newFakeAPI()
	api.createErr = apiError{Code: codes.ResourceExhausted, Message: "machine limit reached"}
	_, err = newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "machine limit reached") || api.execCalls != 0 ||
		errors.Is(err, providers.ErrPermanent) {
		t.Fatalf("create failure = error %v execs %d", err, api.execCalls)
	}
}

func TestBoxdProviderProvisionMarksOnlyRejectedCreatesPermanent(t *testing.T) {
	for _, test := range []struct {
		code      codes.Code
		permanent bool
	}{
		{code: codes.InvalidArgument, permanent: true},
		{code: codes.NotFound, permanent: true},
		{code: codes.PermissionDenied, permanent: true},
		{code: codes.AlreadyExists, permanent: false},
		{code: codes.Unauthenticated, permanent: false},
		{code: codes.FailedPrecondition, permanent: false},
		{code: codes.ResourceExhausted, permanent: false},
		{code: codes.Unavailable, permanent: false},
	} {
		t.Run(test.code.String(), func(t *testing.T) {
			api := newFakeAPI()
			api.createErr = apiError{Code: test.code, Message: "create refused"}
			result, err := newTestProvider(api).ProvisionMachine(
				context.Background(),
				uuid.New(),
				uuid.New(),
				testMachineProvisioning(t, "", ""),
				"machine-token",
				nil,
				true,
			)
			if err == nil || !strings.Contains(err.Error(), "create refused") {
				t.Fatalf("create error = %v", err)
			}
			if errors.Is(err, providers.ErrPermanent) != test.permanent {
				t.Fatalf("create error %v permanent = %t, want %t", err, !test.permanent, test.permanent)
			}
			if result.ProviderResourceID != "" || api.execCalls != 0 || api.destroyCalls != 0 {
				t.Fatalf("refused create side effects = result %+v execs %d destroys %d", result, api.execCalls, api.destroyCalls)
			}
		})
	}
}

func TestBoxdProviderProvisionRejectsUnexpectedOwnership(t *testing.T) {
	api := newFakeAPI()
	api.exists = true
	api.vm.Name = "someone-else"
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "expected allocation name") || api.execCalls != 0 {
		t.Fatalf("ownership error = %v, execs = %d", err, api.execCalls)
	}
}

func TestBoxdProviderProvisionHandlesUnusableMachine(t *testing.T) {
	t.Run("waits for running", func(t *testing.T) {
		api := newFakeAPI()
		api.statuses = []vmStatus{vmStatusPending, vmStatusStarting, "Running"}
		result, err := newTestProvider(api).ProvisionMachine(
			context.Background(),
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "", ""),
			"machine-token",
			nil,
			true,
		)
		if err != nil || result.ProviderResourceID != "vm-123" || api.getCalls != 3 || api.destroyCalls != 0 {
			t.Fatalf(
				"transitional result = %+v error %v gets %d destroys %d",
				result,
				err,
				api.getCalls,
				api.destroyCalls,
			)
		}
	})
	t.Run("timeout while pending", func(t *testing.T) {
		api := newFakeAPI()
		api.vm.Status = vmStatusPending
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		result, err := newTestProvider(api).ProvisionMachine(
			ctx,
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "", ""),
			"machine-token",
			nil,
			true,
		)
		if !errors.Is(err, context.DeadlineExceeded) || result.ProviderResourceID != "vm-123" ||
			api.destroyCalls != 0 || api.execCalls != 0 {
			t.Fatalf("timeout result = %+v error %v destroys %d execs %d", result, err, api.destroyCalls, api.execCalls)
		}
	})
	t.Run("replaces stopped machine", func(t *testing.T) {
		api := newFakeAPI()
		api.vm.Status = vmStatusStopped
		installationID := uuid.New()
		machineID := uuid.New()
		name, err := providers.MachineAllocationName(installationID, machineID)
		require.NoError(t, err)
		result, err := newTestProvider(api).ProvisionMachine(
			context.Background(),
			installationID,
			machineID,
			testMachineProvisioning(t, "", ""),
			"machine-token",
			nil,
			true,
		)
		if !errors.Is(err, providers.ErrResourceReplaced) || result.ProviderResourceID != "" ||
			api.destroyCalls != 1 || api.destroyedRef != name || api.execCalls != 0 {
			t.Fatalf("replace result = %+v error %v destroys %d ref %q", result, err, api.destroyCalls, api.destroyedRef)
		}
	})
	t.Run("dormant machine is usable", func(t *testing.T) {
		api := newFakeAPI()
		api.vm.Status = vmStatusHibernated
		result, err := newTestProvider(api).ProvisionMachine(
			context.Background(),
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "", ""),
			"machine-token",
			nil,
			true,
		)
		if err != nil || result.ProviderResourceID != "vm-123" || api.execCalls != 1 {
			t.Fatalf("dormant result = %+v error %v execs %d", result, err, api.execCalls)
		}
	})
	t.Run("resource mismatch", func(t *testing.T) {
		api := newFakeAPI()
		api.vm.VCPU = 4
		api.vm.MemoryBytes = 16384 * mebibyte
		result, err := newTestProvider(api).ProvisionMachine(
			context.Background(),
			uuid.New(),
			uuid.New(),
			testMachineProvisioning(t, "", ""),
			"machine-token",
			nil,
			true,
		)
		if err == nil || !strings.Contains(
			err.Error(),
			"resources cpu=4 memory_mb=16384 do not match resolved machine resources cpu=2 memory_mb=8192",
		) || result.ProviderResourceID != "vm-123" || api.execCalls != 0 {
			t.Fatalf("resource mismatch = result %+v error %v execs %d", result, err, api.execCalls)
		}
	})
}

func TestBoxdProviderProvisionRetriesBootstrapWhileAgentUnavailable(t *testing.T) {
	api := newFakeAPI()
	api.execErrs = []error{
		apiError{Code: codes.Unavailable, Message: "cannot connect to VM agent"},
		apiError{Code: codes.FailedPrecondition, Message: "waking"},
		nil,
	}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err != nil || result.ProviderResourceID != "vm-123" || api.execCalls != 3 {
		t.Fatalf("retry result = %+v error %v execs %d", result, err, api.execCalls)
	}
	api = newFakeAPI()
	api.execErrs = []error{apiError{Code: codes.PermissionDenied, Message: "VM not accessible"}}
	result, err = newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "VM not accessible") || api.execCalls != 1 ||
		result.ProviderResourceID != "vm-123" {
		t.Fatalf("permanent exec failure = result %+v error %v execs %d", result, err, api.execCalls)
	}
}

func TestBoxdProviderProvisionReportsBootstrapFailure(t *testing.T) {
	api := newFakeAPI()
	api.execResult = execResult{ExitCode: 1}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "", ""),
		"machine-token",
		nil,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "exited with status 1") || result.ProviderResourceID != "vm-123" {
		t.Fatalf("bootstrap failure = result %+v error %v", result, err)
	}
}

func TestBoxdProviderInspectAndDelete(t *testing.T) {
	api := newFakeAPI()
	api.exists = true
	provider := newTestProvider(api)
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(installationID, machineID)
	require.NoError(t, err)
	api.vm.Name = name
	provisioning := testMachineProvisioning(t, "", "")
	resourceID, found, err := provider.InspectMachine(context.Background(), installationID, machineID, provisioning, "")
	if err != nil || !found || resourceID != "vm-123" || api.getLookups[0] != name {
		t.Fatalf("inspect = resource %q found %v error %v lookups %v", resourceID, found, err, api.getLookups)
	}
	err = provider.DeleteMachine(context.Background(), installationID, machineID, provisioning, resourceID)
	if err != nil {
		t.Fatalf("delete boxd machine: %v", err)
	}
	if api.destroyCalls != 1 || api.destroyedRef != resourceID {
		t.Fatalf("destroy = calls %d ref %q", api.destroyCalls, api.destroyedRef)
	}
	api.vm.Name = "someone-else"
	err = provider.DeleteMachine(context.Background(), installationID, machineID, provisioning, resourceID)
	if err == nil || !strings.Contains(err.Error(), "allocation name") || api.destroyCalls != 1 {
		t.Fatalf("foreign machine delete = error %v destroys %d", err, api.destroyCalls)
	}
	api.vm.Name = name
	api.missing = map[string]bool{"stale": true}
	if err := provider.DeleteMachine(context.Background(), installationID, machineID, provisioning, "stale"); err != nil {
		t.Fatalf("delete by allocation name: %v", err)
	}
	if api.destroyCalls != 2 || api.destroyedRef != resourceID {
		t.Fatalf("delete by name = calls %d ref %q", api.destroyCalls, api.destroyedRef)
	}
	api.missing = map[string]bool{"already-absent": true, name: true}
	if err := provider.DeleteMachine(
		context.Background(),
		installationID,
		machineID,
		provisioning,
		"already-absent",
	); err != nil {
		t.Fatalf("delete already absent machine: %v", err)
	}
	if api.destroyCalls != 2 {
		t.Fatalf("already absent machine caused %d destroy calls", api.destroyCalls-2)
	}
	if err := provider.DeleteMachine(context.Background(), installationID, machineID, provisioning, ""); err == nil {
		t.Fatal("expected empty resource id to be rejected")
	}
}
