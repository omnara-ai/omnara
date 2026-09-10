package modal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func testInstallationID() storage.ID {
	return uuid.MustParse("00000000-0000-0000-0000-000000000002")
}

func testOwnershipTags(t *testing.T, machineID storage.ID) map[string]string {
	t.Helper()
	installationOwner, err := publicid.Encode(publicid.KindInstallation, testInstallationID())
	if err != nil {
		t.Fatal(err)
	}
	machineOwner, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{installationTag: installationOwner, machineTag: machineOwner}
}

func testProvisioning(t *testing.T, region string) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu := 1
	memoryMB := 1024
	options := map[string]json.RawMessage{"image": json.RawMessage(`"registry.example/daemon:latest"`)}
	if region != "" {
		options["region"] = json.RawMessage(`"` + region + `"`)
	}
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: options,
	}
}

func testPolicy(
	t *testing.T,
	defaultProvisioning executionstore.MachineProvisioningConfig,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 8192
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: defaultProvisioning,
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: json.RawMessage(`{"app":"omnara"}`),
	}
}

func testSandboxName(t *testing.T, machineID storage.ID) string {
	t.Helper()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	return name
}

type fakeAPI struct {
	byName        map[string]sandbox
	byID          map[string]sandbox
	createRequest createSandboxRequest
	createCalls   int
	createErr     error
	createOnError bool
	deleteCalls   int
	deletedID     string
	getByIDError  error
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{byName: map[string]sandbox{}, byID: map[string]sandbox{}}
}

func (a *fakeAPI) CreateSandbox(_ context.Context, request createSandboxRequest) (sandbox, error) {
	a.createCalls++
	a.createRequest = request
	created := sandbox{
		ID:      "sb-" + request.Name,
		Tags:    request.Tags,
		Running: true,
	}
	if a.createErr == nil || a.createOnError {
		a.byName[request.Name] = created
		a.byID[created.ID] = created
	}
	if a.createErr != nil {
		return sandbox{}, a.createErr
	}
	return created, nil
}

func (a *fakeAPI) GetSandboxByName(_ context.Context, name string) (sandbox, bool, error) {
	target, found := a.byName[name]
	return target, found, nil
}

func (a *fakeAPI) GetSandboxByID(_ context.Context, id string) (sandbox, bool, error) {
	if a.getByIDError != nil {
		return sandbox{}, false, a.getByIDError
	}
	target, found := a.byID[id]
	return target, found, nil
}

func (a *fakeAPI) DeleteSandbox(_ context.Context, id string) error {
	a.deleteCalls++
	a.deletedID = id
	return nil
}

func (a *fakeAPI) Close() {}
