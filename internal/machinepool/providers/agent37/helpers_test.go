package agent37

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const testTemplate = "acme-agent-box"

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
	templateName string,
	cpu, memoryMB int,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: testOptions(t, templateName, ""),
	}
}

func testPoolPolicy(
	t *testing.T,
	config json.RawMessage,
	templateName string,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 16
	maxMemoryMB := 65536
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: testMachineProvisioning(t, templateName, 2, 4096),
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: config,
	}
}

func ownedInstance(t *testing.T, id string, status instanceStatus, installationID, machineID uuid.UUID) instance {
	t.Helper()
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		t.Fatalf("ownership metadata: %v", err)
	}
	return instance{
		ID:        id,
		Status:    status,
		Resources: instanceResources{CPU: 2, MemoryGB: 4},
		Metadata:  map[string]any{installationLabel: metadata[installationLabel], machineLabel: metadata[machineLabel]},
	}
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:          api,
		omnaraAPIURL: "https://api.omnara.test/v1",
		pollDelay:    time.Millisecond,
	}
}

type fakeAPI struct {
	templatesErr   error
	templatesCalls int

	createRequest    createInstanceRequest
	createCalls      int
	createErr        error
	createErrCreates bool
	created          instance

	instances []instance
	listCalls int

	getResults   []instance
	getFound     bool
	getErrs      []error
	getLookups   []string
	getDeadlines []time.Duration

	startErrs  []error
	startCalls int

	deleteCalls int
	deletedID   string

	execIDs      []string
	execCommands []string
	execErrs     []error
	execResults  []execResult
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		created: instance{
			ID:        "a37inst001",
			Status:    instanceStatusRunning,
			Resources: instanceResources{CPU: 2, MemoryGB: 4},
		},
		getFound: true,
	}
}

func (a *fakeAPI) ListTemplates(context.Context) error {
	a.templatesCalls++
	return a.templatesErr
}

func (a *fakeAPI) CreateInstance(_ context.Context, request createInstanceRequest) (instance, error) {
	a.createCalls++
	a.createRequest = request
	if a.created.Metadata == nil {
		a.created.Metadata = map[string]any{}
		for key, value := range request.Metadata {
			a.created.Metadata[key] = value
		}
	}
	if a.createErr != nil {
		if a.createErrCreates {
			a.instances = append(a.instances, a.created)
		}
		return instance{}, a.createErr
	}
	return a.created, nil
}

func (a *fakeAPI) ListInstances(context.Context) ([]instance, error) {
	a.listCalls++
	return a.instances, nil
}

func (a *fakeAPI) GetInstance(ctx context.Context, id string) (instance, bool, error) {
	a.getLookups = append(a.getLookups, id)
	if deadline, ok := ctx.Deadline(); ok {
		a.getDeadlines = append(a.getDeadlines, time.Until(deadline))
	}
	if len(a.getErrs) > 0 {
		err := a.getErrs[0]
		a.getErrs = a.getErrs[1:]
		return instance{}, false, err
	}
	if len(a.getResults) > 0 {
		next := a.getResults[0]
		a.getResults = a.getResults[1:]
		return next, a.getFound, nil
	}
	return a.created, a.getFound, nil
}

func (a *fakeAPI) StartInstance(context.Context, string) error {
	a.startCalls++
	if len(a.startErrs) > 0 {
		err := a.startErrs[0]
		a.startErrs = a.startErrs[1:]
		return err
	}
	return nil
}

func (a *fakeAPI) DeleteInstance(_ context.Context, id string) error {
	a.deleteCalls++
	a.deletedID = id
	return nil
}

func (a *fakeAPI) Exec(_ context.Context, id, command string) (execResult, error) {
	a.execIDs = append(a.execIDs, id)
	a.execCommands = append(a.execCommands, command)
	if len(a.execErrs) > 0 {
		err := a.execErrs[0]
		a.execErrs = a.execErrs[1:]
		if err != nil {
			return execResult{}, err
		}
	}
	if len(a.execResults) > 0 {
		next := a.execResults[0]
		a.execResults = a.execResults[1:]
		return next, nil
	}
	return execResult{}, nil
}

var _ apiClient = (*fakeAPI)(nil)
