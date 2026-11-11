package maritime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func testOptions(t *testing.T, templateName, startupScript string) map[string]json.RawMessage {
	t.Helper()
	options := map[string]json.RawMessage{}
	if templateName != "" {
		options["template"] = mustRawJSON(t, templateName)
	}
	if startupScript != "" {
		options["startup_script"] = mustRawJSON(t, startupScript)
	}
	return options
}

func testMachineProvisioning(
	t *testing.T,
	templateName, startupScript string,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu := 2
	memoryMB := 4096
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: testOptions(t, templateName, startupScript),
	}
}

func testPoolPolicy(
	t *testing.T,
	config json.RawMessage,
	templateName string,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 16384
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: testMachineProvisioning(t, templateName, ""),
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: config,
	}
}

func mustOwnershipMetadata(t *testing.T, installationID, machineID uuid.UUID) map[string]string {
	t.Helper()
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		t.Fatalf("ownership metadata: %v", err)
	}
	return metadata
}

func ownedSandbox(t *testing.T, id string, state sandboxState, installationID, machineID uuid.UUID) sandbox {
	t.Helper()
	return sandbox{
		ID:           id,
		State:        state,
		Resources:    sandboxResources{VCPU: 2, MemoryMiB: 4096, DiskGiB: 12},
		DataPlaneURL: "https://api-" + id + ".sbx.maritime.test/v1",
		Secret:       "secret-1",
		Metadata:     mustOwnershipMetadata(t, installationID, machineID),
	}
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:          api,
		omnaraAPIURL: "https://api.omnara.test/v1",
		pollDelay:    time.Millisecond,
	}
}

func exitCode(code int) *int {
	return &code
}

type fakeAPI struct {
	checkErr error

	createRequests []createSandboxRequest
	createErr      error
	created        sandbox

	sandboxes  []sandbox
	listCalls  int
	listFilter map[string]string

	getResults []sandbox
	getFound   bool
	getErrs    []error
	getLookups []string

	deleteCalls int
	deletedID   string

	runTargets  []sandbox
	runRequests []runRequest
	runErrs     []error
	runResults  []command
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		created: sandbox{
			ID:           "sbx-abc123",
			State:        sandboxStateRunning,
			Resources:    sandboxResources{VCPU: 2, MemoryMiB: 4096, DiskGiB: 12},
			DataPlaneURL: "https://api-sbx-abc123.sbx.maritime.test/v1",
			Secret:       "secret-1",
		},
		getFound: true,
	}
}

func (a *fakeAPI) CheckConnection(context.Context) error {
	return a.checkErr
}

func (a *fakeAPI) CreateSandbox(_ context.Context, request createSandboxRequest) (sandbox, error) {
	a.createRequests = append(a.createRequests, request)
	if a.createErr != nil {
		return sandbox{}, a.createErr
	}
	created := a.created
	if created.Metadata == nil {
		created.Metadata = request.Metadata
	}
	return created, nil
}

func (a *fakeAPI) ListSandboxes(_ context.Context, metadata map[string]string) ([]sandbox, error) {
	a.listCalls++
	a.listFilter = metadata
	var matching []sandbox
	for _, current := range a.sandboxes {
		matches := true
		for key, value := range metadata {
			if current.Metadata[key] != value {
				matches = false
			}
		}
		if matches {
			matching = append(matching, current)
		}
	}
	return matching, nil
}

func (a *fakeAPI) GetSandbox(_ context.Context, id string) (sandbox, bool, error) {
	a.getLookups = append(a.getLookups, id)
	if len(a.getErrs) > 0 {
		err := a.getErrs[0]
		a.getErrs = a.getErrs[1:]
		if err != nil {
			return sandbox{}, false, err
		}
	}
	if len(a.getResults) > 0 {
		next := a.getResults[0]
		a.getResults = a.getResults[1:]
		return next, a.getFound, nil
	}
	return a.created, a.getFound, nil
}

func (a *fakeAPI) DeleteSandbox(_ context.Context, id string) error {
	a.deleteCalls++
	a.deletedID = id
	return nil
}

func (a *fakeAPI) Run(_ context.Context, target sandbox, request runRequest) (command, error) {
	a.runTargets = append(a.runTargets, target)
	a.runRequests = append(a.runRequests, request)
	if len(a.runErrs) > 0 {
		err := a.runErrs[0]
		a.runErrs = a.runErrs[1:]
		if err != nil {
			return command{}, err
		}
	}
	if len(a.runResults) > 0 {
		next := a.runResults[0]
		a.runResults = a.runResults[1:]
		return next, nil
	}
	return command{State: "exited", ExitCode: exitCode(0)}, nil
}

var _ apiClient = (*fakeAPI)(nil)
