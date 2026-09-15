package freestyle

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestProviderProvisionCreatesResizesAndStartsManagedDaemon(t *testing.T) {
	exitCode := 0
	created := ownedVM(t, vmStateRunning, 1, 2048)
	resized := ownedVM(t, vmStateRunning, 2, 4096)
	api := &fakeAPI{
		createResult: created,
		resizeResult: resized,
		execResponse: execVMResponse{StatusCode: &exitCode},
	}

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		map[string]string{"APP_ENV": "production"},
	)
	if err != nil {
		t.Fatalf("provision freestyle VM: %v", err)
	}
	if result.ProviderResourceID != created.ID {
		t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, created.ID)
	}
	name, err := machineName()
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	request := api.createRequest
	if request.SnapshotID != "ubuntu-24.04" || request.Slug != name || request.DisplayName != name ||
		request.AutoDeleteSeconds != autoDeleteNever || !request.AutomaticRestart {
		t.Fatalf("create request = %+v", request)
	}
	if request.Metadata[providerMarkerKey] != providerMarkerValue ||
		request.Metadata[installationMarkerKey] != testInstallationID().String() ||
		request.Metadata[machineMarkerKey] != testMachineID().String() {
		t.Fatalf("create ownership metadata = %#v", request.Metadata)
	}
	if len(request.Firewall.Rules) != 1 || request.Firewall.Rules[0].Action != "allow" ||
		!request.Firewall.Rules[0].Destination.Public {
		t.Fatalf("create firewall = %+v", request.Firewall)
	}
	if api.resizeRequest.CPU != 2 || api.resizeRequest.MemoryMB != 4096 {
		t.Fatalf("resize request = %+v", api.resizeRequest)
	}
	if api.execCalls != 1 || api.execResourceID != created.ID ||
		api.execRequest.LinuxUser != "root" || api.execRequest.TimeoutMS != daemonInstallTimeoutMS {
		t.Fatalf("exec request = resource %q request %+v", api.execResourceID, api.execRequest)
	}
	if strings.Contains(api.execRequest.Command, "machine-token") ||
		!strings.Contains(api.execRequest.Command, "systemctl enable "+daemonServiceName) {
		t.Fatalf("managed daemon install command = %q", api.execRequest.Command)
	}
	startScript := decodeInstallStartScript(t, api.execRequest.Command)
	for _, want := range []string{
		"APP_ENV=production",
		"OMNARA_API_URL=https://api.omnara.test/v1",
		"OMNARA_MACHINE_TOKEN=machine-token",
		providers.ManagedBootstrapScriptEnvVar + "=",
	} {
		if !strings.Contains(startScript, want) {
			t.Fatalf("managed daemon start script missing %q: %s", want, startScript)
		}
	}
}

func TestProviderProvisionAdoptsOwnedVMAfterAmbiguousCreate(t *testing.T) {
	exitCode := 0
	existing := ownedVM(t, vmStateRunning, 2, 4096)
	api := &fakeAPI{
		createErr:    errors.New("ambiguous transport failure"),
		execResponse: execVMResponse{StatusCode: &exitCode},
	}
	api.getFunc = func(string) (vm, bool, error) {
		if len(api.getLookups) == 1 {
			return vm{}, false, nil
		}
		return existing, true, nil
	}

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		nil,
	)
	if err != nil || result.ProviderResourceID != existing.ID {
		t.Fatalf("adopt existing VM = %+v, error %v", result, err)
	}
	if len(api.getLookups) != 2 || api.execCalls != 1 {
		t.Fatalf("adoption lookups = %#v, exec calls = %d", api.getLookups, api.execCalls)
	}
}

func TestProviderProvisionRejectsForeignVMBeforeMutation(t *testing.T) {
	foreign := ownedVM(t, vmStateRunning, 2, 4096)
	foreign.Metadata[machineMarkerKey] = "someone-else"
	api := &fakeAPI{getFunc: func(string) (vm, bool, error) { return foreign, true, nil }}

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "expected ownership metadata") {
		t.Fatalf("foreign VM error = %v", err)
	}
	if result.ProviderResourceID != foreign.ID || api.startCalls != 0 || api.execCalls != 0 {
		t.Fatalf("foreign VM result = %+v starts = %d execs = %d", result, api.startCalls, api.execCalls)
	}
}

func TestProviderWakeStartsOwnedPausedVM(t *testing.T) {
	paused := ownedVM(t, vmStatePaused, 2, 4096)
	api := &fakeAPI{
		getFunc:     func(string) (vm, bool, error) { return paused, true, nil },
		startResult: ownedVM(t, vmStateStarting, 2, 4096),
	}
	err := newTestProvider(api).WakeMachine(context.Background(), providers.WakeMachineInput{
		ProviderResourceID: paused.ID,
	})
	if err != nil || api.startCalls != 1 {
		t.Fatalf("wake paused VM error = %v, start calls = %d", err, api.startCalls)
	}
}

func TestProviderDeleteChecksOwnership(t *testing.T) {
	foreign := ownedVM(t, vmStateRunning, 2, 4096)
	foreign.Metadata[installationMarkerKey] = "someone-else"
	api := &fakeAPI{getFunc: func(string) (vm, bool, error) { return foreign, true, nil }}
	err := newTestProvider(api).DeleteMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		foreign.ID,
	)
	if err == nil || !strings.Contains(err.Error(), "expected ownership metadata") || api.deleteCalls != 0 {
		t.Fatalf("foreign delete error = %v, delete calls = %d", err, api.deleteCalls)
	}
}

func decodeInstallStartScript(t *testing.T, command string) string {
	t.Helper()
	const prefix = "printf '%s' '"
	const suffix = "'|base64 -d >\"$d/start\""
	start := strings.Index(command, prefix)
	if start < 0 {
		t.Fatalf("install command has no start-script payload: %q", command)
	}
	start += len(prefix)
	end := strings.Index(command[start:], suffix)
	if end < 0 {
		t.Fatalf("install command has malformed start-script payload: %q", command)
	}
	raw, err := base64.StdEncoding.DecodeString(command[start : start+end])
	if err != nil {
		t.Fatalf("decode start-script payload: %v", err)
	}
	return string(raw)
}
