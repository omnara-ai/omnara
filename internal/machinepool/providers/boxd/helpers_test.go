package boxd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const testStartupScriptEnvVar = "OMNARA_STARTUP_SCRIPT_PAYLOAD"

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func testOptions(t *testing.T, snapshot, startupScript string) map[string]json.RawMessage {
	t.Helper()
	options := map[string]json.RawMessage{
		"startup_script": mustRawJSON(t, startupScript),
	}
	if snapshot != "" {
		options["snapshot"] = mustRawJSON(t, snapshot)
	}
	return options
}

func testSleepOptions(t *testing.T, sleepAfterMS int) map[string]json.RawMessage {
	t.Helper()
	options := testOptions(t, "", "")
	options["sleep_after_ms"] = mustRawJSON(t, sleepAfterMS)
	return options
}

func testSleepProvisioning(t *testing.T, sleepAfterMS int) executionstore.MachineProvisioningConfig {
	t.Helper()
	provisioning := testMachineProvisioning(t, "", "")
	provisioning.ProviderOptions = testSleepOptions(t, sleepAfterMS)
	return provisioning
}

func testMachineProvisioning(
	t *testing.T,
	snapshot, startupScript string,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu := 2
	memoryMB := 8192
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: testOptions(t, snapshot, startupScript),
	}
}

func boxdPolicyForTest(
	t *testing.T,
	defaultSnapshot string,
	providerConfig json.RawMessage,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 32768
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			ProviderOptions: testOptions(t, defaultSnapshot, ""),
		},
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: providerConfig,
	}
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:          api,
		omnaraAPIURL: "https://api.omnara.test/v1",
	}
}

type fakeAPI struct {
	vm       vm
	exists   bool
	statuses []vmStatus

	getErr     error
	getCalls   int
	getLookups []string
	missing    map[string]bool

	createErr            error
	createCalls          int
	createRequest        createVMRequest
	existsAfterCreateErr bool

	listVMs      []vm
	listErr      error
	listCalls    int
	listDeadline time.Time

	destroyCalls int
	destroyedRef string

	execErrs     []error
	execResult   execResult
	execCalls    int
	execRef      string
	execCommand  string
	execStdin    []byte
	execDeadline time.Time

	snapshot      snapshotInfo
	snapshotFound bool
	snapshotErr   error
	snapshotName  string

	orgDefaults    machineSize
	orgDefaultsErr error
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		vm: vm{
			ID:           "vm-123",
			Status:       vmStatusRunning,
			VCPU:         2,
			MemoryBytes:  8192 * mebibyte,
			AccessDomain: "boxd.sh",
		},
	}
}

func (a *fakeAPI) GetVM(_ context.Context, ref string) (vm, bool, error) {
	a.getCalls++
	a.getLookups = append(a.getLookups, ref)
	if a.getErr != nil {
		return vm{}, false, a.getErr
	}
	if a.missing[ref] || !a.exists {
		return vm{}, false, nil
	}
	if len(a.statuses) > 0 {
		a.vm.Status = a.statuses[0]
		a.statuses = a.statuses[1:]
	}
	return a.vm, true, nil
}

func (a *fakeAPI) ListVMs(ctx context.Context) ([]vm, error) {
	a.listCalls++
	a.listDeadline, _ = ctx.Deadline()
	if a.listErr != nil {
		return nil, a.listErr
	}
	return a.listVMs, nil
}

func (a *fakeAPI) CreateVM(_ context.Context, request createVMRequest) (vm, error) {
	a.createCalls++
	a.createRequest = request
	if a.vm.Name == "" {
		a.vm.Name = request.Name
	}
	if a.createErr != nil {
		a.exists = a.existsAfterCreateErr
		return vm{}, a.createErr
	}
	a.exists = true
	created := vm{ID: a.vm.ID, Name: a.vm.Name, Status: a.vm.Status}
	if len(a.statuses) > 0 {
		created.Status = a.statuses[0]
		a.statuses = a.statuses[1:]
	}
	return created, nil
}

func (a *fakeAPI) DestroyVM(_ context.Context, ref string) error {
	a.destroyCalls++
	a.destroyedRef = ref
	return nil
}

func (a *fakeAPI) Exec(
	ctx context.Context,
	ref string,
	command string,
	stdin []byte,
) (execResult, error) {
	a.execCalls++
	a.execDeadline, _ = ctx.Deadline()
	a.execRef = ref
	a.execCommand = command
	a.execStdin = stdin
	if len(a.execErrs) > 0 {
		err := a.execErrs[0]
		a.execErrs = a.execErrs[1:]
		if err != nil {
			return execResult{}, err
		}
	}
	return a.execResult, nil
}

func (a *fakeAPI) GetSnapshot(_ context.Context, name string) (snapshotInfo, bool, error) {
	a.snapshotName = name
	if a.snapshotErr != nil {
		return snapshotInfo{}, false, a.snapshotErr
	}
	return a.snapshot, a.snapshotFound, nil
}

func (a *fakeAPI) GetOrgMachineDefaults(context.Context) (machineSize, error) {
	return a.orgDefaults, a.orgDefaultsErr
}

var _ apiClient = (*fakeAPI)(nil)
