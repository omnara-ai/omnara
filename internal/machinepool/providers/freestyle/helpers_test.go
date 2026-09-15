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
	name, err := machineName()
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	return vm{
		ID:                         "vm-123",
		State:                      state,
		Slug:                       name,
		SourceSnapshotSlugAtCreate: "ubuntu-24.04",
		Resources:                  vmResources{CPU: cpu, MemoryMB: memoryMB},
		Metadata:                   ownershipMetadata(testInstallationID(), testMachineID()),
	}
}

func machineName() (string, error) {
	return providers.MachineAllocationName(testInstallationID(), testMachineID())
}

type fakeAPI struct {
	getFunc        func(string) (vm, bool, error)
	getLookups     []string
	createRequest  createVMRequest
	createResult   vm
	createErr      error
	resizeRequest  resizeVMRequest
	resizeResult   vm
	resizeErr      error
	startCalls     int
	startResult    vm
	startErr       error
	deleteCalls    int
	deletedID      string
	deleteErr      error
	execCalls      int
	execResourceID string
	execRequest    execVMRequest
	execResponse   execVMResponse
	execErr        error
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

func (a *fakeAPI) ResizeVM(_ context.Context, _ string, request resizeVMRequest) (vm, error) {
	a.resizeRequest = request
	return a.resizeResult, a.resizeErr
}

func (a *fakeAPI) StartVM(context.Context, string) (vm, error) {
	a.startCalls++
	return a.startResult, a.startErr
}

func (a *fakeAPI) DeleteVM(_ context.Context, id string) error {
	a.deleteCalls++
	a.deletedID = id
	return a.deleteErr
}

func (a *fakeAPI) ExecVM(
	_ context.Context,
	resourceID string,
	request execVMRequest,
) (execVMResponse, error) {
	a.execCalls++
	a.execResourceID = resourceID
	a.execRequest = request
	return a.execResponse, a.execErr
}

func newTestProvider(api apiClient) *provider {
	return &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
}

var _ apiClient = (*fakeAPI)(nil)
