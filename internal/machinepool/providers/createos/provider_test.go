package createos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestCreateOSPrepareProvisioningKeepsConfiguredResources(t *testing.T) {
	api := newFakeAPI()
	provisioning := testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", "")
	cpu := 4
	memoryMB := 8192
	provisioning.CPU = &cpu
	provisioning.MemoryMB = &memoryMB
	facts, err := newTestProvider(api).PrepareProvisioning(context.Background(), provisioning)
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
	api.shapes = []Shape{
		{ID: "s-1vcpu-1gb", VCPU: 1, MemMiB: 1024},
		{ID: "s-2vcpu-4gb", VCPU: 2, MemMiB: 4096},
	}
	facts, err := newTestProvider(api).PrepareProvisioning(
		context.Background(),
		testMachineProvisioning(t, "s-2vcpu-4gb", "devbox:1", "us", ""),
	)
	if err != nil {
		t.Fatalf("prepare provisioning: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != 2 || facts.MemoryMB == nil || *facts.MemoryMB != 4096 {
		t.Fatalf("resource facts = %+v, want the catalog values for the shape", facts)
	}
}

func TestCreateOSPrepareProvisioningRejectsUnknownShape(t *testing.T) {
	api := newFakeAPI()
	api.shapes = []Shape{{ID: "s-1vcpu-1gb", VCPU: 1, MemMiB: 1024}}
	_, err := newTestProvider(api).PrepareProvisioning(
		context.Background(),
		testMachineProvisioning(t, "s-9vcpu-9gb", "devbox:1", "us", ""),
	)
	if err == nil || !strings.Contains(err.Error(), "s-9vcpu-9gb") {
		t.Fatalf("prepare provisioning error = %v, want the shape to be reported as missing", err)
	}
}

func TestCreateOSPrepareProvisioningRejectsShapeWithoutResources(t *testing.T) {
	api := newFakeAPI()
	api.shapes = []Shape{{ID: "s-1vcpu-1gb"}}
	_, err := newTestProvider(api).PrepareProvisioning(
		context.Background(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
	)
	if err == nil {
		t.Fatal("prepare provisioning accepted a shape with no cpu or memory")
	}
}

func TestCreateOSProvisionMachineCreatesSandboxAndStartsDaemon(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	provisioning := testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", "")
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		provisioning,
		"machine-token",
		map[string]string{"TEAM": "platform"},
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" {
		t.Fatalf("provider resource id = %q, want sb-123", result.ProviderResourceID)
	}
	if api.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", api.createCalls)
	}
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	if api.createRequest.Name != name {
		t.Fatalf("create name = %q, want %q", api.createRequest.Name, name)
	}
	if api.createRequest.Shape != "s-1vcpu-1gb" ||
		api.createRequest.RootFS != "devbox:1" ||
		api.createRequest.Region != "us" {
		t.Fatalf("create request = %+v, want the provisioning options", api.createRequest)
	}
	wantEnv := []string{
		"OMNARA_API_URL",
		"OMNARA_MACHINE_TOKEN",
		providers.ManagedBootstrapScriptEnvVar,
		"TEAM",
	}
	for _, key := range wantEnv {
		if api.createRequest.Envs[key] == "" {
			t.Fatalf("create env is missing %q: %+v", key, api.createRequest.Envs)
		}
	}
	if api.createProcessCalls != 1 {
		t.Fatalf("create process calls = %d, want 1", api.createProcessCalls)
	}
	args := providers.ManagedDaemonLauncherArgs()
	if api.createProcessRequest.Command != args[0] {
		t.Fatalf("daemon command = %q, want %q", api.createProcessRequest.Command, args[0])
	}
	if api.createProcessTarget != "sb-123" {
		t.Fatalf("daemon sandbox = %q, want sb-123", api.createProcessTarget)
	}
}

func TestCreateOSProvisionMachineAdoptsExistingSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.listPages = []sandboxPage{{items: []sandbox{{
		ID:     "sb-existing",
		Name:   name,
		Status: sandboxStatusRunning,
		Shape:  "s-1vcpu-1gb",
		RootFS: "devbox:1",
		Region: "us",
	}}, total: 1}}
	api.processes = []process{{ID: "proc-1", State: "running"}}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-existing" {
		t.Fatalf("provider resource id = %q, want sb-existing", result.ProviderResourceID)
	}
	if api.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0 for an adopted sandbox", api.createCalls)
	}
	if api.createProcessCalls != 0 {
		t.Fatalf("create process calls = %d, want 0 while the daemon is running", api.createProcessCalls)
	}
}

func TestCreateOSProvisionMachineRestartsDaemonWhenLeaderExited(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.listPages = []sandboxPage{{items: []sandbox{{
		ID:     "sb-existing",
		Name:   name,
		Status: sandboxStatusRunning,
		Shape:  "s-1vcpu-1gb",
		RootFS: "devbox:1",
		Region: "us",
	}}, total: 1}}
	api.processes = []process{{ID: "proc-1", State: "running", LeaderExited: true}}
	if _, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	); err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if api.createProcessCalls != 1 {
		t.Fatalf("create process calls = %d, want 1 after the daemon leader exited", api.createProcessCalls)
	}
}

func TestCreateOSProvisionMachineRejectsMismatchedSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.listPages = []sandboxPage{{items: []sandbox{{
		ID:     "sb-existing",
		Name:   name,
		Status: sandboxStatusRunning,
		Shape:  "s-4vcpu-8gb",
		RootFS: "devbox:1",
		Region: "us",
	}}, total: 1}}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err == nil {
		t.Fatal("provision machine accepted a sandbox with a different shape")
	}
	if result.ProviderResourceID != "sb-existing" {
		t.Fatalf("provider resource id = %q, want the observed id to survive the error", result.ProviderResourceID)
	}
}

func TestCreateOSProvisionMachineRejectsDuplicateAllocationNames(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	duplicate := sandbox{Name: name, Status: sandboxStatusRunning}
	first := duplicate
	first.ID = "sb-a"
	second := duplicate
	second.ID = "sb-b"
	api.listPages = []sandboxPage{{items: []sandbox{first, second}, total: 2}}
	_, err = newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("provision machine error = %v, want the duplicate allocation to be reported", err)
	}
}

func TestCreateOSProvisionMachineIgnoresTerminatedSandboxes(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.listPages = []sandboxPage{{items: []sandbox{
		{ID: "sb-old", Name: name, Status: sandboxStatusDestroyed},
		{ID: "sb-dead", Name: name, Status: sandboxStatusFailed},
	}, total: 2}}
	if _, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	); err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if api.createCalls != 1 {
		t.Fatalf("create calls = %d, want a fresh sandbox", api.createCalls)
	}
}

func TestCreateOSProvisionMachinePaginatesSandboxLookup(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	filler := make([]sandbox, 500)
	for index := range filler {
		filler[index] = sandbox{ID: "other", Name: "omnara-other", Status: sandboxStatusRunning}
	}
	api.listPages = []sandboxPage{
		{items: filler, total: 501},
		{items: []sandbox{{
			ID:     "sb-page-two",
			Name:   name,
			Status: sandboxStatusRunning,
			Shape:  "s-1vcpu-1gb",
			RootFS: "devbox:1",
			Region: "us",
		}}, total: 501},
	}
	api.processes = []process{{ID: "proc-1", State: "running"}}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-page-two" {
		t.Fatalf("provider resource id = %q, want the match from the second page", result.ProviderResourceID)
	}
	if len(api.listQueries) != 2 || api.listQueries[1][1] != 500 {
		t.Fatalf("list queries = %v, want a second page at offset 500", api.listQueries)
	}
}

func TestCreateOSProvisionMachineKeepsResourceIDWhenReadinessFails(t *testing.T) {
	api := newFakeAPI()
	api.getErr = errors.New("control plane is unavailable")
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err == nil {
		t.Fatal("provision machine hid the readiness failure")
	}
	if result.ProviderResourceID != "sb-123" {
		t.Fatalf("provider resource id = %q, want the created id to survive the error", result.ProviderResourceID)
	}
}

func TestCreateOSProvisionMachineAdoptsAfterAmbiguousCreateFailure(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.createErr = errors.New("connection reset")
	api.listPages = []sandboxPage{
		{},
		{items: []sandbox{{
			ID:     "sb-raced",
			Name:   name,
			Status: sandboxStatusRunning,
			Shape:  "s-1vcpu-1gb",
			RootFS: "devbox:1",
			Region: "us",
		}}, total: 1},
	}
	api.processes = []process{{ID: "proc-1", State: "starting"}}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-raced" {
		t.Fatalf("provider resource id = %q, want the raced sandbox to be adopted", result.ProviderResourceID)
	}
}

func TestCreateOSProvisionMachineWaitsForRunningSandbox(t *testing.T) {
	api := newFakeAPI()
	api.created = sandbox{
		ID:     "sb-123",
		Status: sandboxStatusCreating,
		Shape:  "s-1vcpu-1gb",
		RootFS: "devbox:1",
		Region: "us",
	}
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("provision machine error = %v, want the sandbox to be reported as not running", err)
	}
	if api.createProcessCalls != 0 {
		t.Fatalf("create process calls = %d, want the daemon to wait for a running sandbox", api.createProcessCalls)
	}
}

func TestCreateOSInspectMachineMatchesAllocationName(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.created = sandbox{ID: "sb-123", Name: name, Status: sandboxStatusRunning}
	id, found, err := newTestProvider(api).InspectMachine(
		context.Background(),
		installationID,
		machineID,
		executionstore.MachineProvisioningConfig{},
		"sb-123",
	)
	if err != nil || !found || id != "sb-123" {
		t.Fatalf("inspect machine = %q found %v error %v", id, found, err)
	}
}

func TestCreateOSInspectMachineRejectsForeignSandbox(t *testing.T) {
	api := newFakeAPI()
	api.created = sandbox{ID: "sb-123", Name: "omnara-someone-else", Status: sandboxStatusRunning}
	_, found, err := newTestProvider(api).InspectMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		executionstore.MachineProvisioningConfig{},
		"sb-123",
	)
	if err == nil || found {
		t.Fatalf("inspect machine = found %v error %v, want a refusal", found, err)
	}
}

func TestCreateOSInspectMachineFallsBackToAllocationLookup(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.listPages = []sandboxPage{{items: []sandbox{{ID: "sb-found", Name: name, Status: sandboxStatusRunning}}, total: 1}}
	id, found, err := newTestProvider(api).InspectMachine(
		context.Background(),
		installationID,
		machineID,
		executionstore.MachineProvisioningConfig{},
		"",
	)
	if err != nil || !found || id != "sb-found" {
		t.Fatalf("inspect machine = %q found %v error %v", id, found, err)
	}
	if len(api.getLookups) != 0 {
		t.Fatalf("sandbox lookups = %v, want the allocation listing to be used", api.getLookups)
	}
}

func TestCreateOSDeleteMachineRequiresResourceID(t *testing.T) {
	api := newFakeAPI()
	err := newTestProvider(api).DeleteMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		executionstore.MachineProvisioningConfig{},
		"  ",
	)
	if err == nil {
		t.Fatal("delete machine accepted an empty resource id")
	}
	if api.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", api.deleteCalls)
	}
}

func TestCreateOSDeleteMachineDeletesOwnedSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	api.created = sandbox{ID: "sb-123", Name: name, Status: sandboxStatusRunning}
	if err := newTestProvider(api).DeleteMachine(
		context.Background(),
		installationID,
		machineID,
		executionstore.MachineProvisioningConfig{},
		"sb-123",
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	if api.deleteCalls != 1 || api.deletedID != "sb-123" {
		t.Fatalf("delete calls = %d for %q, want one call for sb-123", api.deleteCalls, api.deletedID)
	}
}

func TestCreateOSDeleteMachineIsQuietWhenSandboxIsGone(t *testing.T) {
	api := newFakeAPI()
	api.getFound = false
	if err := newTestProvider(api).DeleteMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		executionstore.MachineProvisioningConfig{},
		"sb-123",
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	if api.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0 for an absent sandbox", api.deleteCalls)
	}
}

func TestCreateOSAllocationNameIsStableAndScoped(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	first, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	second, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	if first != second {
		t.Fatalf("allocation names %q and %q differ for the same machine", first, second)
	}
	other, err := allocationName(installationID, uuid.New())
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	if other == first {
		t.Fatal("two machines share one allocation name")
	}
	if !strings.HasPrefix(first, "omnara-") || len(first) != len("omnara-")+15 {
		t.Fatalf("allocation name = %q, want a 15 character omnara- prefixed name", first)
	}
}

func TestCreateOSProvisioningTimeoutIsBounded(t *testing.T) {
	if got := newTestProvider(newFakeAPI()).ProvisioningTimeout(); got != provisioningTimeout {
		t.Fatalf("provisioning timeout = %s, want %s", got, provisioningTimeout)
	}
}

func TestCreateOSProvisionMachineAdoptsTheRegionCreateOSPicked(t *testing.T) {
	api := newFakeAPI()
	api.created = sandbox{
		ID:     "sb-123",
		Status: sandboxStatusRunning,
		Shape:  "s-1vcpu-1gb",
		RootFS: "devbox:1",
		Region: "eu",
	}
	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "", ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" {
		t.Fatalf("provider resource id = %q, want sb-123", result.ProviderResourceID)
	}
	if _, sent := api.createRequest.Envs["OMNARA_API_URL"]; !sent {
		t.Fatal("create request did not carry the machine environment")
	}
	if api.createRequest.Region != "" {
		t.Fatalf("create region = %q, want it left to CreateOS", api.createRequest.Region)
	}
}

func TestCreateOSProvisionMachineStillChecksARequestedRegion(t *testing.T) {
	api := newFakeAPI()
	api.created = sandbox{
		ID:     "sb-123",
		Status: sandboxStatusRunning,
		Shape:  "s-1vcpu-1gb",
		RootFS: "devbox:1",
		Region: "eu",
	}
	_, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		uuid.New(),
		uuid.New(),
		testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "us", ""),
		"machine-token",
		nil,
	)
	if err == nil {
		t.Fatal("provision machine accepted a sandbox in the wrong region")
	}
}
