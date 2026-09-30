package freestyle

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return raw
}

func testProvisioning(t *testing.T) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu := 2
	memoryMB := 4096
	return executionstore.MachineProvisioningConfig{
		CPU:      &cpu,
		MemoryMB: &memoryMB,
		ProviderOptions: map[string]json.RawMessage{
			"snapshot":       rawJSON(t, "ubuntu-24.04"),
			"startup_script": rawJSON(t, "echo ready"),
		},
	}
}

func testInstallationID() uuid.UUID {
	return uuid.MustParse("10000000-0000-0000-0000-000000000001")
}

func testMachineID() uuid.UUID {
	return uuid.MustParse("20000000-0000-0000-0000-000000000002")
}

func ownedVM(t *testing.T, state string, cpu, memoryMB int) vm {
	t.Helper()
	owned := ownedVMFor(t, testMachineID(), "vm-123", state)
	owned.Resources = vmResources{CPU: cpu, MemoryMB: memoryMB}
	return owned
}

func ownedVMFor(t *testing.T, machineID uuid.UUID, id, state string) vm {
	t.Helper()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	metadata, err := ownershipMetadata(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("ownership metadata: %v", err)
	}
	return vm{ID: id, State: state, Slug: name, Metadata: metadata}
}

func machineName() (string, error) {
	return providers.MachineAllocationName(testInstallationID(), testMachineID())
}

type fakeAPI struct {
	getFunc        func(string) (vm, bool, error)
	getLookups     []string
	listPages      []vmList
	listErr        error
	listFilters    []string
	listOffsets    []int
	createRequest  createVMRequest
	createResult   vm
	createErr      error
	resizeRequest  resizeVMRequest
	startCalls     int
	startResult    vm
	deleteCalls    int
	execCalls      int
	execResourceID string
	execRequest    execVMRequest
	execResponse   execVMResponse
}

func (a *fakeAPI) CreateVM(_ context.Context, request createVMRequest) (vm, error) {
	a.createRequest = request
	return a.createResult, a.createErr
}

func (a *fakeAPI) GetVM(_ context.Context, lookup string) (vm, bool, error) {
	a.getLookups = append(a.getLookups, lookup)
	if a.getFunc == nil {
		return vm{}, false, nil
	}
	return a.getFunc(lookup)
}

func (a *fakeAPI) ListVMs(_ context.Context, metadata string, _, offset int) (vmList, error) {
	a.listFilters = append(a.listFilters, metadata)
	a.listOffsets = append(a.listOffsets, offset)
	if a.listErr != nil {
		return vmList{}, a.listErr
	}
	page := len(a.listOffsets) - 1
	if page >= len(a.listPages) {
		return vmList{}, nil
	}
	return a.listPages[page], nil
}

func (a *fakeAPI) ResizeVM(_ context.Context, _ string, request resizeVMRequest) error {
	a.resizeRequest = request
	return nil
}

func (a *fakeAPI) StartVM(context.Context, string) (vm, error) {
	a.startCalls++
	return a.startResult, nil
}

func (a *fakeAPI) DeleteVM(context.Context, string) error {
	a.deleteCalls++
	return nil
}

func (a *fakeAPI) ExecVM(
	_ context.Context,
	resourceID string,
	request execVMRequest,
) (execVMResponse, error) {
	a.execCalls++
	a.execResourceID = resourceID
	a.execRequest = request
	return a.execResponse, nil
}

func newTestProvider(api apiClient) *provider {
	return &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
}

var _ apiClient = (*fakeAPI)(nil)
