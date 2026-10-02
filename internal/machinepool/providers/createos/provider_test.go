package createos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func provisionTestMachine(
	t *testing.T,
	api *fakeAPI,
	installationID, machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineEnv map[string]string,
) (providers.ProvisionMachineResult, error) {
	t.Helper()
	return newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		machineProvisioning,
		"machine-token",
		machineEnv,
		true,
	)
}

func daemonProcess(id, state string, leaderExited bool) process {
	args := daemonLauncherArgs()
	return process{ID: id, Command: args[0], Args: args[1:], State: state, LeaderExited: leaderExited}
}

func TestCreateOSPrepareProvisioningKeepsConfiguredResources(t *testing.T) {
	api := newFakeAPI()
	machineProvisioning := testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "")
	cpu := 4
	memoryMB := 8192
	machineProvisioning.CPU = &cpu
	machineProvisioning.MemoryMB = &memoryMB
	facts, err := newTestProvider(api).PrepareProvisioning(context.Background(), machineProvisioning)
	if err != nil {
		t.Fatalf("prepare provisioning: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != cpu || facts.MemoryMB == nil || *facts.MemoryMB != memoryMB {
		t.Fatalf("resource facts = %+v, want the configured values", facts)
	}
	if api.shapeCalls != 0 {
		t.Fatalf("shape catalog calls = %d, want 0", api.shapeCalls)
	}
}

func TestCreateOSPrepareProvisioningResolvesShapeResources(t *testing.T) {
	api := newFakeAPI()
	api.shapes = []sandboxShape{
		{ID: "s-0.25vcpu-512mb", VCPU: 1, MemMiB: 512},
		{ID: "s-2vcpu-4gb", VCPU: 2, MemMiB: 4096},
	}
	for shape, want := range map[string][2]int{
		"s-0.25vcpu-512mb": {1, 512},
		"s-2vcpu-4gb":      {2, 4096},
	} {
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			testMachineProvisioning(t, shape, "devbox:1", ""),
		)
		if err != nil {
			t.Fatalf("prepare provisioning %s: %v", shape, err)
		}
		if facts.CPU == nil || *facts.CPU != want[0] || facts.MemoryMB == nil || *facts.MemoryMB != want[1] {
			t.Fatalf("%s resource facts = %+v, want cpu %d memory_mb %d", shape, facts, want[0], want[1])
		}
	}
}

func TestCreateOSPrepareProvisioningRejectsUnknownOrEmptyShapes(t *testing.T) {
	api := newFakeAPI()
	api.shapes = []sandboxShape{{ID: "s-1vcpu-1gb", VCPU: 1, MemMiB: 1024}, {ID: "s-broken"}}
	for shape, want := range map[string]string{
		"s-9vcpu-9gb": "was not found",
		"s-broken":    "invalid resources",
	} {
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			testMachineProvisioning(t, shape, "devbox:1", ""),
		)
		if err == nil || !strings.Contains(err.Error(), want) || errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
			t.Fatalf("prepare provisioning %s error = %v, want %q", shape, err, want)
		}
	}
}

func TestCreateOSPrepareProvisioningClassifiesProviderFailures(t *testing.T) {
	for name, test := range map[string]struct {
		err         error
		unavailable bool
	}{
		"transport":    {err: errors.New("connection reset"), unavailable: true},
		"rate limited": {err: apiError{StatusCode: http.StatusTooManyRequests}, unavailable: true},
		"server error": {err: apiError{StatusCode: http.StatusBadGateway}, unavailable: true},
		"unauthorized": {err: apiError{StatusCode: http.StatusUnauthorized}},
	} {
		api := newFakeAPI()
		api.shapesErr = test.err
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		)
		if err == nil || errors.Is(err, storeerr.ErrMachineProviderUnavailable) != test.unavailable {
			t.Fatalf("%s: prepare provisioning error = %v, want unavailable %t", name, err, test.unavailable)
		}
	}
}

func TestCreateOSValidateMachineConfigEnforcesSandboxEnvLimits(t *testing.T) {
	manyEntries := map[string]string{}
	for index := range 61 {
		manyEntries[fmt.Sprintf("VAR_%d", index)] = "value"
	}
	largeTotal := map[string]string{}
	for index := range 17 {
		largeTotal[fmt.Sprintf("LARGE_%d", index)] = strings.Repeat("a", 4000)
	}
	for name, test := range map[string]struct {
		env  map[string]string
		want string
	}{
		"ordinary env":          {env: map[string]string{"GITHUB_TOKEN": "token", "my-var": "dropped"}},
		"largest value":         {env: map[string]string{"KEY": strings.Repeat("a", 4096)}},
		"too many entries":      {env: manyEntries, want: "at most 64 entries"},
		"oversized value":       {env: map[string]string{"KEY": strings.Repeat("a", 4097)}, want: "env KEY must be"},
		"oversized total":       {env: largeTotal, want: "total at most"},
		"dropped invalid names": {env: map[string]string{"my-var": strings.Repeat("a", 5000)}},
	} {
		err := newTestProvider(newFakeAPI()).ValidateMachineConfig(
			testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
			test.env,
		)
		if test.want == "" && err != nil {
			t.Fatalf("%s: validate machine config: %v", name, err)
		}
		if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
			t.Fatalf("%s: validate machine config error = %v, want %q", name, err, test.want)
		}
	}
}

func TestCreateOSValidateSandboxEnvCountsCreateOSEntryOverhead(t *testing.T) {
	env := map[string]string{}
	for index := range 15 {
		env[fmt.Sprintf("K%02d", index)] = strings.Repeat("a", 4096)
	}
	used := 0
	for key, value := range env {
		used += len(key) + len(value) + sandboxEnvEntryOverhead
	}
	env["FILL"] = strings.Repeat("b", maxSandboxEnvBytes-used-len("FILL")-sandboxEnvEntryOverhead)
	if err := validateSandboxEnv(env); err != nil {
		t.Fatalf("validate env at the 64 KiB limit: %v", err)
	}
	env["FILL"] += "b"
	if err := validateSandboxEnv(env); err == nil || !strings.Contains(err.Error(), "total at most") {
		t.Fatalf("validate env over the limit error = %v, want the total limit", err)
	}
}

func TestCreateOSProvisionMachineCreatesSandboxAndStartsDaemon(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	result, err := provisionTestMachine(
		t,
		api,
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "echo ready"),
		map[string]string{"TEAM": "platform", "my-var": "dropped"},
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" || result.SandboxURL != "" {
		t.Fatalf("provision result = %+v, want sb-123 without a sandbox url", result)
	}
	request := api.createRequest
	if api.createCalls != 1 || request.Name != mustAllocationName(t, installationID, machineID) ||
		request.Shape != "s-1vcpu-1gb" || request.RootFS != "devbox:1" {
		t.Fatalf("create calls = %d request = %+v", api.createCalls, request)
	}
	for key, want := range map[string]string{
		"OMNARA_API_URL":                       "https://api.omnara.test/v1",
		"OMNARA_INSTALLER_URL":                 "https://api.omnara.test/install/omnarad.sh",
		"OMNARA_MACHINE_TOKEN":                 "machine-token",
		"OMNARA_STARTUP_SCRIPT_PAYLOAD":        "ZWNobyByZWFkeQ==",
		providers.ManagedBootstrapScriptEnvVar: providers.ManagedBootScriptPayload(),
		"TEAM":                                 "platform",
	} {
		if request.Envs[key] != want {
			t.Fatalf("create env %s = %q, want %q", key, request.Envs[key], want)
		}
	}
	if _, ok := request.Envs["my-var"]; ok || len(request.Envs) != 6 {
		t.Fatalf("create env = %v, want only CreateOS-compatible names", request.Envs)
	}
	if !slices.Equal(api.getLookups, []string{"sb-123"}) {
		t.Fatalf("sandbox lookups = %v, want the created sandbox read back", api.getLookups)
	}
	launcher := providers.ManagedDaemonLauncherArgs()
	processRequest := api.createProcessRequest
	if api.createProcessCalls != 1 || api.createProcessTarget != "sb-123" ||
		processRequest.Command != launcher[0] || len(processRequest.Args) != 2 ||
		processRequest.Args[0] != launcher[1] ||
		processRequest.Args[1] != strings.TrimSpace(guestDevFixupScript)+"\n"+launcher[2] {
		t.Fatalf("daemon process calls = %d target = %q request = %+v",
			api.createProcessCalls, api.createProcessTarget, processRequest)
	}
}

func TestCreateOSProvisionMachineLooksUpOnlyLiveSandboxes(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name := mustAllocationName(t, installationID, machineID)
	api.sandboxes = []sandbox{
		{ID: "sb-destroyed", Name: name, Status: sandboxStatusDestroyed},
		{ID: "sb-failed", Name: name, Status: sandboxStatusFailed},
	}
	if _, err := provisionTestMachine(
		t,
		api,
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	); err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if api.createCalls != 1 {
		t.Fatalf("create calls = %d, want terminal sandboxes ignored", api.createCalls)
	}
	var statuses []sandboxStatus
	for _, query := range api.listQueries {
		if query.limit != sandboxListPageSize || query.offset != 0 {
			t.Fatalf("list query = %+v, want one full page per status", query)
		}
		statuses = append(statuses, query.status)
	}
	if !slices.Equal(statuses, liveSandboxStatuses[:]) {
		t.Fatalf("listed statuses = %v, want %v", statuses, liveSandboxStatuses)
	}
}

func TestCreateOSProvisionMachineRejectsOversizedEnvAsPermanent(t *testing.T) {
	api := newFakeAPI()
	_, err := provisionTestMachine(
		t,
		api,
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		map[string]string{"PRIVATE_KEY": strings.Repeat("a", 4097)},
	)
	if !errors.Is(err, providers.ErrPermanent) || api.createCalls != 0 {
		t.Fatalf("provision error = %v create calls = %d, want a permanent error before create", err, api.createCalls)
	}
}

func TestCreateOSProvisionMachineAdoptsExistingSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.sandboxes = []sandbox{{
		ID:     "sb-existing",
		Name:   mustAllocationName(t, installationID, machineID),
		Status: sandboxStatusRunning,
		Shape:  "s-1vcpu-1gb",
	}}
	api.processes = []process{daemonProcess("proc-1", processStateRunning, false)}
	result, err := provisionTestMachine(
		t,
		api,
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-existing" || api.createCalls != 0 || api.createProcessCalls != 0 {
		t.Fatalf("result = %+v create calls = %d process calls = %d, want adoption without side effects",
			result, api.createCalls, api.createProcessCalls)
	}
}

func TestCreateOSProvisionMachineWaitsForACreatingSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	starting := sandbox{
		ID:     "sb-123",
		Name:   mustAllocationName(t, installationID, machineID),
		Status: sandboxStatusCreating,
		Shape:  "s-1vcpu-1gb",
	}
	api.sandboxes = []sandbox{starting}
	api.getResults = []sandbox{starting}
	api.created.Name = starting.Name
	result, err := provisionTestMachine(
		t,
		api,
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" || api.createCalls != 0 || api.createProcessCalls != 1 ||
		!slices.Equal(api.getLookups, []string{"sb-123", "sb-123"}) {
		t.Fatalf("result = %+v create calls = %d process calls = %d lookups = %v, want the sandbox awaited until running",
			result, api.createCalls, api.createProcessCalls, api.getLookups)
	}
}

func TestCreateOSProvisionMachineGivesUpOnASandboxThatFailsToStart(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name := mustAllocationName(t, installationID, machineID)
	api.sandboxes = []sandbox{{ID: "sb-123", Name: name, Status: sandboxStatusCreating, Shape: "s-1vcpu-1gb"}}
	api.created = sandbox{ID: "sb-123", Name: name, Status: sandboxStatusFailed, Shape: "s-1vcpu-1gb"}
	result, err := provisionTestMachine(
		t,
		api,
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	)
	if !errors.Is(err, providers.ErrPermanent) || result.ProviderResourceID != "sb-123" ||
		api.createCalls != 0 || api.createProcessCalls != 0 {
		t.Fatalf("result = %+v error = %v create calls = %d process calls = %d, want a permanent failure",
			result, err, api.createCalls, api.createProcessCalls)
	}
}

func TestCreateOSProvisionMachineStartsDaemonWhenNoneIsRunning(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.sandboxes = []sandbox{{
		ID:     "sb-existing",
		Name:   mustAllocationName(t, installationID, machineID),
		Status: sandboxStatusRunning,
		Shape:  "s-1vcpu-1gb",
	}}
	api.processes = []process{
		daemonProcess("proc-old", processStateRunning, true),
		daemonProcess("proc-done", "exited", true),
		{ID: "proc-other", Command: "/bin/sh", Args: []string{"-c", "sleep 600"}, State: processStateRunning},
	}
	if _, err := provisionTestMachine(
		t,
		api,
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	); err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if api.createProcessCalls != 1 || api.createProcessTarget != "sb-existing" {
		t.Fatalf("daemon process calls = %d target = %q, want a new daemon", api.createProcessCalls, api.createProcessTarget)
	}
}

func TestCreateOSProvisionMachineReportsUnusableAdoptedSandboxes(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	name := mustAllocationName(t, installationID, machineID)
	for label, test := range map[string]struct {
		existing sandbox
		want     string
	}{
		"shape mismatch": {
			existing: sandbox{ID: "sb-1", Name: name, Status: sandboxStatusRunning, Shape: "s-2vcpu-4gb"},
			want:     "does not match",
		},
		"paused": {
			existing: sandbox{ID: "sb-1", Name: name, Status: sandboxStatusPaused, Shape: "s-1vcpu-1gb"},
			want:     "is not running",
		},
	} {
		api := newFakeAPI()
		api.sandboxes = []sandbox{test.existing}
		result, err := provisionTestMachine(
			t,
			api,
			installationID,
			machineID,
			testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
			nil,
		)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: provision error = %v, want %q", label, err, test.want)
		}
		if result.ProviderResourceID != "sb-1" || api.createCalls != 0 || api.createProcessCalls != 0 {
			t.Fatalf("%s: result = %+v create calls = %d process calls = %d, want the owned id kept",
				label, result, api.createCalls, api.createProcessCalls)
		}
	}
}

func TestCreateOSProvisionMachinePaginatesSandboxLookup(t *testing.T) {
	api := newFakeAPI()
	api.pageLimit = 1
	installationID := uuid.New()
	machineID := uuid.New()
	api.sandboxes = []sandbox{
		{ID: "sb-other-1", Name: "omnara-other-1", Status: sandboxStatusRunning},
		{ID: "sb-other-2", Name: "omnara-other-2", Status: sandboxStatusRunning},
		{
			ID:     "sb-existing",
			Name:   mustAllocationName(t, installationID, machineID),
			Status: sandboxStatusRunning,
			Shape:  "s-1vcpu-1gb",
		},
	}
	api.processes = []process{daemonProcess("proc-1", processStateStarting, false)}
	result, err := provisionTestMachine(
		t,
		api,
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-existing" || api.createCalls != 0 {
		t.Fatalf("result = %+v create calls = %d, want the sandbox on the last page adopted", result, api.createCalls)
	}
	var runningOffsets []int
	for _, query := range api.listQueries {
		if query.status == sandboxStatusRunning {
			runningOffsets = append(runningOffsets, query.offset)
		}
	}
	if !slices.Equal(runningOffsets, []int{0, 1, 2}) {
		t.Fatalf("running list offsets = %v, want every page read", runningOffsets)
	}
}

func TestCreateOSProvisionMachineAdoptsAfterAmbiguousCreateFailure(t *testing.T) {
	api := newFakeAPI()
	api.createErr = errors.New("connection reset")
	api.createErrCreates = true
	api.processes = []process{daemonProcess("proc-1", processStateStarting, false)}
	result, err := provisionTestMachine(
		t,
		api,
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" || api.createCalls != 1 {
		t.Fatalf("result = %+v create calls = %d, want the created sandbox adopted", result, api.createCalls)
	}
}

func TestCreateOSProvisionMachineReturnsCreateErrorWhenNothingWasCreated(t *testing.T) {
	api := newFakeAPI()
	api.createErr = apiError{StatusCode: http.StatusTooManyRequests}
	result, err := provisionTestMachine(
		t,
		api,
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	)
	if !errors.Is(err, api.createErr) || result.ProviderResourceID != "" {
		t.Fatalf("result = %+v error = %v, want the create error without a resource id", result, err)
	}
}

func TestCreateOSProvisionMachineDoesNotReturnForeignSandboxIDs(t *testing.T) {
	api := newFakeAPI()
	api.getResults = []sandbox{{ID: "sb-123", Name: "omnara-someone", Status: sandboxStatusRunning}}
	result, err := provisionTestMachine(
		t,
		api,
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", ""),
		nil,
	)
	if err == nil || result.ProviderResourceID != "" || api.createProcessCalls != 0 {
		t.Fatalf("result = %+v error = %v, want a foreign sandbox refused", result, err)
	}
}

func TestCreateOSInspectMachineChecksTheAllocationName(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	name := mustAllocationName(t, installationID, machineID)

	api := newFakeAPI()
	api.created = sandbox{ID: "sb-123", Name: name, Status: sandboxStatusRunning}
	id, found, err := newTestProvider(api).InspectMachine(
		context.Background(), installationID, machineID, executionstore.MachineProvisioningConfig{}, "sb-123",
	)
	if err != nil || !found || id != "sb-123" {
		t.Fatalf("inspect by id = %q found %v error %v", id, found, err)
	}

	api.created.Name = "omnara-someone"
	if _, found, err := newTestProvider(api).InspectMachine(
		context.Background(), installationID, machineID, executionstore.MachineProvisioningConfig{}, "sb-123",
	); err == nil || found {
		t.Fatalf("inspect foreign sandbox = found %v error %v, want a refusal", found, err)
	}

	api = newFakeAPI()
	api.sandboxes = []sandbox{{ID: "sb-found", Name: name, Status: sandboxStatusPaused}}
	id, found, err = newTestProvider(api).InspectMachine(
		context.Background(), installationID, machineID, executionstore.MachineProvisioningConfig{}, "",
	)
	if err != nil || !found || id != "sb-found" || len(api.getLookups) != 0 {
		t.Fatalf("inspect by name = %q found %v error %v lookups %v", id, found, err, api.getLookups)
	}
}

func TestCreateOSDeleteMachine(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	name := mustAllocationName(t, installationID, machineID)
	deleteMachine := func(api *fakeAPI, resourceID string) error {
		return newTestProvider(api).DeleteMachine(
			context.Background(), installationID, machineID, executionstore.MachineProvisioningConfig{}, resourceID,
		)
	}

	if err := deleteMachine(newFakeAPI(), ""); err == nil {
		t.Fatal("delete machine accepted an empty resource id")
	}

	api := newFakeAPI()
	api.created = sandbox{ID: "sb-123", Name: name, Status: sandboxStatusRunning}
	if err := deleteMachine(api, "sb-123"); err != nil || api.deletedID != "sb-123" {
		t.Fatalf("delete owned sandbox error = %v deleted %q", err, api.deletedID)
	}

	api = newFakeAPI()
	api.getFound = false
	api.sandboxes = []sandbox{{ID: "sb-replacement", Name: name, Status: sandboxStatusRunning}}
	if err := deleteMachine(api, "sb-stale"); err != nil || api.deletedID != "sb-replacement" {
		t.Fatalf("delete by name fallback error = %v deleted %q", err, api.deletedID)
	}

	api = newFakeAPI()
	api.created = sandbox{ID: "sb-dead", Name: name, Status: sandboxStatusFailed}
	api.sandboxes = []sandbox{{ID: "sb-live", Name: name, Status: sandboxStatusRunning}}
	if err := deleteMachine(api, "sb-dead"); err != nil || api.deletedID != "sb-live" {
		t.Fatalf("delete past a dead sandbox error = %v deleted %q, want the live one by name", err, api.deletedID)
	}

	api = newFakeAPI()
	api.getFound = false
	if err := deleteMachine(api, "sb-gone"); err != nil || api.deleteCalls != 0 {
		t.Fatalf("delete missing sandbox error = %v delete calls %d, want a quiet no-op", err, api.deleteCalls)
	}
}

func TestCreateOSAllocationNameIsStableScopedAndShort(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	name := mustAllocationName(t, installationID, machineID)
	if name != mustAllocationName(t, installationID, machineID) {
		t.Fatal("allocation name is not stable")
	}
	if len(name) > 22 || !strings.HasPrefix(name, "omnara-") {
		t.Fatalf("allocation name = %q, want an omnara- name within CreateOS's 22 characters", name)
	}
	if name == mustAllocationName(t, uuid.New(), machineID) || name == mustAllocationName(t, installationID, uuid.New()) {
		t.Fatal("allocation name is not scoped to the installation and machine")
	}
}
