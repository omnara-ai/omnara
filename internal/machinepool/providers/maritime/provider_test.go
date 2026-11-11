package maritime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestMaritimeProvisionCreatesANamedSandboxAndLaunchesTheDaemon(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	installationID, machineID := uuid.New(), uuid.New()
	config := testMachineProvisioning(t, "", "echo ready")

	result, err := provider.ProvisionMachine(
		context.Background(), installationID, machineID, config, "machine-token",
		map[string]string{"CUSTOM": "value", "not-a-valid-name": "dropped"}, true,
	)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if result.ProviderResourceID != "sbx-abc123" {
		t.Fatalf("resource id = %q, want sbx-abc123", result.ProviderResourceID)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create calls = %d, want 1", len(api.createRequests))
	}
	request := api.createRequests[0]
	wantName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	if request.Name != wantName || request.From != defaultTemplate || request.VCPU != 2 ||
		request.MemoryMiB != 4096 || request.DiskGiB != 0 || !request.KeepAwake {
		t.Fatalf("create request = %+v", request)
	}
	wantMetadata := mustOwnershipMetadata(t, installationID, machineID)
	if request.Metadata[installationLabel] != wantMetadata[installationLabel] ||
		request.Metadata[machineLabel] != wantMetadata[machineLabel] {
		t.Fatalf("create metadata = %v, want %v", request.Metadata, wantMetadata)
	}
	if len(api.runRequests) != 1 {
		t.Fatalf("run calls = %d, want 1", len(api.runRequests))
	}
	run := api.runRequests[0]
	if len(run.Cmd) != 5 || run.Cmd[0] != "/bin/sh" || run.Cmd[3] != "omnara-launch" ||
		run.Cmd[4] != providers.ManagedDaemonLauncherArgs()[2] || run.TimeoutSeconds != daemonRunTimeoutSeconds {
		t.Fatalf("run request = %+v", run)
	}
	if !strings.Contains(run.Cmd[2], ".omnara-maritime") {
		t.Fatalf("launcher script does not use the maritime state directory")
	}
	if run.Env["OMNARA_MACHINE_TOKEN"] != "machine-token" || run.Env["CUSTOM"] != "value" ||
		run.Env[providers.ManagedBootstrapScriptEnvVar] == "" {
		t.Fatalf("run env is missing managed values: %v", run.Env)
	}
	if _, ok := run.Env["not-a-valid-name"]; ok {
		t.Fatal("run env kept a name Maritime rejects")
	}
	if api.runTargets[0].Secret != "secret-1" {
		t.Fatalf("run used secret %q", api.runTargets[0].Secret)
	}
}

func TestMaritimeProvisionRetriesConvergeOnTheSameSandbox(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	installationID, machineID := uuid.New(), uuid.New()
	config := testMachineProvisioning(t, "", "")
	for _, firstAttempt := range []bool{true, false} {
		result, err := provider.ProvisionMachine(
			context.Background(), installationID, machineID, config, "token", nil, firstAttempt,
		)
		if err != nil || result.ProviderResourceID != "sbx-abc123" {
			t.Fatalf("provision (first attempt %v) = %+v, %v", firstAttempt, result, err)
		}
	}
	if api.createRequests[0].Name != api.createRequests[1].Name {
		t.Fatalf("retries used different names: %q and %q", api.createRequests[0].Name, api.createRequests[1].Name)
	}
}

func TestMaritimeProvisionPassesTemplateAndDisk(t *testing.T) {
	api := newFakeAPI()
	api.created.Resources.DiskGiB = 40
	provider := newTestProvider(api)
	config := testMachineProvisioning(t, "my-template", "")
	config.ProviderOptions["disk_gb"] = mustRawJSON(t, 40)
	if _, err := provider.ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), config, "token", nil, true,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if request := api.createRequests[0]; request.From != "my-template" || request.DiskGiB != 40 {
		t.Fatalf("create request = %+v", request)
	}
}

func TestMaritimeProvisionCreateRejectionsArePermanent(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		permanent bool
	}{
		{"plan required", apiError{StatusCode: http.StatusPaymentRequired, Code: "plan_required"}, true},
		{"invalid request", apiError{StatusCode: http.StatusBadRequest, Code: "invalid_request"}, true},
		{"unknown template", apiError{StatusCode: http.StatusNotFound, Code: "snapshot_not_found"}, true},
		{"missing scope", apiError{StatusCode: http.StatusForbidden, Code: "scope_missing"}, true},
		{"rate limited", apiError{StatusCode: http.StatusTooManyRequests, Code: "rate_limited"}, false},
		{"concurrency limit", apiError{StatusCode: http.StatusTooManyRequests, Code: "concurrency_limit"}, false},
		{"out of capacity", apiError{StatusCode: http.StatusServiceUnavailable, Code: "out_of_capacity"}, false},
		{"transport", errors.New("connection reset"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newFakeAPI()
			api.createErr = test.err
			_, err := newTestProvider(api).ProvisionMachine(
				context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, true,
			)
			if err == nil {
				t.Fatal("provision succeeded")
			}
			if got := errors.Is(err, providers.ErrPermanent); got != test.permanent {
				t.Fatalf("permanent = %v, want %v: %v", got, test.permanent, err)
			}
			if len(api.runRequests) != 0 {
				t.Fatal("launched the daemon after a failed create")
			}
		})
	}
}

func TestMaritimeProvisionRefusesASandboxOwnedByAnotherMachine(t *testing.T) {
	api := newFakeAPI()
	api.created.Metadata = mustOwnershipMetadata(t, uuid.New(), uuid.New())
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, true,
	)
	if !errors.Is(err, providers.ErrPermanent) || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("provision error = %v, want a permanent ownership error", err)
	}
	if len(api.runRequests) != 0 {
		t.Fatal("launched the daemon on a sandbox Omnara does not own")
	}
}

func TestMaritimeProvisionRejectsResourceMismatch(t *testing.T) {
	api := newFakeAPI()
	api.created.Resources.VCPU = 4
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, true,
	)
	if !errors.Is(err, providers.ErrPermanent) || result.ProviderResourceID != "sbx-abc123" {
		t.Fatalf("provision = %+v, %v; want the resource id and a permanent error", result, err)
	}
}

func TestMaritimeProvisionWaitsForTheSandboxToStart(t *testing.T) {
	api := newFakeAPI()
	starting := api.created
	starting.State = sandboxStateStarting
	api.created = starting
	running := starting
	running.State = sandboxStateRunning
	api.getResults = []sandbox{starting, running}
	api.getErrs = []error{apiError{StatusCode: http.StatusServiceUnavailable}}
	if _, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, true,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(api.getLookups) != 3 || len(api.runRequests) != 1 {
		t.Fatalf("get lookups = %d, runs = %d; want 3 and 1", len(api.getLookups), len(api.runRequests))
	}
}

func TestMaritimeProvisionLaunchesOnAPausedSandboxWithoutWaiting(t *testing.T) {
	api := newFakeAPI()
	api.created.State = sandboxStatePaused
	if _, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, false,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(api.getLookups) != 0 || len(api.runRequests) != 1 {
		t.Fatalf("get lookups = %d, runs = %d; want 0 and 1", len(api.getLookups), len(api.runRequests))
	}
}

func TestMaritimeProvisionReportsAnEndedSandboxAsReplaced(t *testing.T) {
	api := newFakeAPI()
	api.created.State = sandboxStateStarting
	ended := api.created
	ended.State = sandboxStateEnded
	ended.EndedReason = "create_failed"
	api.getResults = []sandbox{ended}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, true,
	)
	if !errors.Is(err, providers.ErrResourceReplaced) || result.ProviderResourceID != "sbx-abc123" {
		t.Fatalf("provision = %+v, %v; want ErrResourceReplaced with the observed id", result, err)
	}
}

func TestMaritimeProvisionRefreshesARotatedSecretOnce(t *testing.T) {
	api := newFakeAPI()
	api.runErrs = []error{apiError{StatusCode: http.StatusUnauthorized, Code: "unauthenticated"}}
	refreshed := api.created
	refreshed.Secret = "secret-2"
	api.getResults = []sandbox{refreshed}
	if _, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, true,
	); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if len(api.runTargets) != 2 || api.runTargets[1].Secret != "secret-2" {
		t.Fatalf("run targets = %+v, want a retry with the refreshed secret", api.runTargets)
	}
}

func TestMaritimeProvisionReportsLauncherFailure(t *testing.T) {
	api := newFakeAPI()
	api.runResults = []command{{State: "exited", ExitCode: exitCode(1), Stderr: "omnara daemon exited early\n401"}}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "token", nil, true,
	)
	if err == nil || !strings.Contains(err.Error(), "omnara daemon exited early") ||
		result.ProviderResourceID != "sbx-abc123" {
		t.Fatalf("provision = %+v, %v; want the launcher output and the resource id", result, err)
	}
}

func TestMaritimeValidateMachineConfigEnforcesEnvLimits(t *testing.T) {
	provider := newTestProvider(newFakeAPI())
	config := testMachineProvisioning(t, "", "")
	if err := provider.ValidateMachineConfig(config, map[string]string{"SMALL": "value"}); err != nil {
		t.Fatalf("validate small env: %v", err)
	}
	if err := provider.ValidateMachineConfig(
		config, map[string]string{"LARGE": strings.Repeat("x", maxEnvValueBytes+1)},
	); err == nil || !strings.Contains(err.Error(), "LARGE") {
		t.Fatalf("validate large value = %v, want an error naming LARGE", err)
	}
	if err := provider.ValidateMachineConfig(
		testMachineProvisioning(t, "", strings.Repeat("x", 30*1024)), nil,
	); err == nil || !strings.Contains(err.Error(), "startup_script") {
		t.Fatalf("validate long startup script = %v, want a startup_script error", err)
	}
	many := map[string]string{}
	for index := range maxEnvKeys {
		many["KEY_"+strings.Repeat("A", index%5)+string(rune('A'+index%26))+string(rune('A'+index/26))] = "v"
	}
	if err := provider.ValidateMachineConfig(config, many); err == nil {
		t.Fatal("validate too many env keys succeeded")
	}
	total := map[string]string{}
	for index := range 9 {
		total["CHUNK_"+string(rune('A'+index))] = strings.Repeat("x", maxEnvValueBytes-1)
	}
	if err := provider.ValidateMachineConfig(config, total); err == nil {
		t.Fatal("validate env over the total limit succeeded")
	}
}

func TestMaritimeInspectMachine(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	api := newFakeAPI()
	api.created = ownedSandbox(t, "sbx-abc123", sandboxStateRunning, installationID, machineID)
	api.sandboxes = []sandbox{
		ownedSandbox(t, "sbx-old", sandboxStateEnded, installationID, machineID),
		api.created,
		ownedSandbox(t, "sbx-other", sandboxStateRunning, installationID, uuid.New()),
	}
	provider := newTestProvider(api)
	config := testMachineProvisioning(t, "", "")

	id, found, err := provider.InspectMachine(context.Background(), installationID, machineID, config, "")
	if err != nil || !found || id != "sbx-abc123" {
		t.Fatalf("inspect by ownership = %q, %v, %v", id, found, err)
	}
	if api.listFilter[machineLabel] == "" || api.listFilter[installationLabel] == "" {
		t.Fatalf("list filter = %v, want both ownership labels", api.listFilter)
	}
	id, found, err = provider.InspectMachine(context.Background(), installationID, machineID, config, "sbx-abc123")
	if err != nil || !found || id != "sbx-abc123" {
		t.Fatalf("inspect by id = %q, %v, %v", id, found, err)
	}
	if _, _, err := provider.InspectMachine(
		context.Background(), uuid.New(), machineID, config, "sbx-abc123",
	); err == nil {
		t.Fatal("inspect with another installation succeeded")
	}
	api.created.State = sandboxStateEnded
	if _, found, err := provider.InspectMachine(
		context.Background(), installationID, machineID, config, "sbx-abc123",
	); err != nil || found {
		t.Fatalf("inspect ended sandbox = found %v, %v", found, err)
	}
	api.getFound = false
	if _, found, err := provider.InspectMachine(
		context.Background(), installationID, machineID, config, "sbx-gone",
	); err != nil || found {
		t.Fatalf("inspect missing sandbox = found %v, %v", found, err)
	}
}

func TestMaritimeInspectRejectsDuplicateOwnership(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	api := newFakeAPI()
	api.sandboxes = []sandbox{
		ownedSandbox(t, "sbx-a", sandboxStateRunning, installationID, machineID),
		ownedSandbox(t, "sbx-b", sandboxStatePaused, installationID, machineID),
	}
	if _, _, err := newTestProvider(api).InspectMachine(
		context.Background(), installationID, machineID, testMachineProvisioning(t, "", ""), "",
	); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("inspect = %v, want a duplicate ownership error", err)
	}
}

func TestMaritimeDeleteMachine(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	config := testMachineProvisioning(t, "", "")

	api := newFakeAPI()
	api.created = ownedSandbox(t, "sbx-abc123", sandboxStateRunning, installationID, machineID)
	if err := newTestProvider(api).DeleteMachine(
		context.Background(), installationID, machineID, config, "sbx-abc123",
	); err != nil || api.deletedID != "sbx-abc123" {
		t.Fatalf("delete = %v, deleted %q", err, api.deletedID)
	}

	api = newFakeAPI()
	api.created = ownedSandbox(t, "sbx-abc123", sandboxStateRunning, installationID, machineID)
	if err := newTestProvider(api).DeleteMachine(
		context.Background(), uuid.New(), machineID, config, "sbx-abc123",
	); err == nil || api.deleteCalls != 0 {
		t.Fatalf("delete with another installation = %v, delete calls %d", err, api.deleteCalls)
	}

	api = newFakeAPI()
	api.getFound = false
	if err := newTestProvider(api).DeleteMachine(
		context.Background(), installationID, machineID, config, "sbx-gone",
	); err != nil || api.deleteCalls != 0 || api.listCalls != 1 {
		t.Fatalf("delete missing = %v, delete calls %d, list calls %d", err, api.deleteCalls, api.listCalls)
	}

	if err := newTestProvider(newFakeAPI()).DeleteMachine(
		context.Background(), installationID, machineID, config, "",
	); err == nil {
		t.Fatal("delete without a resource id succeeded")
	}
}

func TestMaritimeCheckConnection(t *testing.T) {
	api := newFakeAPI()
	api.checkErr = apiError{StatusCode: http.StatusUnauthorized}
	if err := newTestProvider(api).CheckConnection(
		context.Background(), testMachineProvisioning(t, "", ""),
	); err == nil {
		t.Fatal("check connection succeeded with a rejected key")
	}
}
