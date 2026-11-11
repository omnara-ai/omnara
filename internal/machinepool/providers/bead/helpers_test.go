package bead

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type fakeAPI struct {
	templates   []template
	templateErr error
	shapes      []machineShape
	shapeErr    error
	sandboxes   map[string]sandbox
	creates     []createSandboxRequest
	createErr   error
	hide        bool
	findErr     error

	starts        []startProcessRequest
	startErr      error
	startUnlisted bool
	tags          map[string][]string
	deleted       []string
}

func (f *fakeAPI) ListShapes(context.Context) ([]machineShape, error) {
	if f.shapeErr != nil {
		return nil, f.shapeErr
	}
	if f.shapes != nil {
		return f.shapes, nil
	}
	return []machineShape{{CPU: 1, MemoryMB: 2048}, {CPU: 2, MemoryMB: 4096}, {CPU: 4, MemoryMB: 8192}}, nil
}

func (f *fakeAPI) CreateSandbox(_ context.Context, request createSandboxRequest) (sandbox, error) {
	f.creates = append(f.creates, request)
	if f.createErr != nil {
		return sandbox{}, f.createErr
	}
	created := f.addSandbox(fmt.Sprintf("sbx%d", len(f.creates)), request.Metadata, 2, 4096)
	return sandbox{SandboxID: created.SandboxID, EnvdAccessToken: created.EnvdAccessToken}, nil
}

func (f *fakeAPI) addSandbox(id string, metadata map[string]string, cpu, memoryMB int) sandbox {
	if f.sandboxes == nil {
		f.sandboxes = map[string]sandbox{}
	}
	created := sandbox{
		SandboxID:       id,
		State:           sandboxStateRunning,
		CPUCount:        cpu,
		MemoryMB:        memoryMB,
		Metadata:        metadata,
		EnvdAccessToken: "envd-" + id,
	}
	f.sandboxes[id] = created
	return created
}

func (f *fakeAPI) FindSandboxes(_ context.Context, metadata map[string]string) ([]sandbox, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	var found []sandbox
	for _, current := range f.sandboxes {
		if !f.hide && ownedBy(current, metadata) {
			found = append(found, sandbox{SandboxID: current.SandboxID, State: current.State, Metadata: current.Metadata})
		}
	}
	return found, nil
}

func (f *fakeAPI) GetSandbox(_ context.Context, id string) (sandbox, bool, error) {
	current, found := f.sandboxes[id]
	return current, found, nil
}

func (f *fakeAPI) DeleteSandbox(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.sandboxes, id)
	return nil
}

func (f *fakeAPI) ListProcesses(_ context.Context, target sandbox) ([]process, error) {
	var processes []process
	for _, tag := range f.tags[target.SandboxID] {
		processes = append(processes, process{Tag: tag})
	}
	return processes, nil
}

func (f *fakeAPI) StartProcess(_ context.Context, target sandbox, request startProcessRequest) error {
	f.starts = append(f.starts, request)
	if f.startUnlisted {
		return f.startErr
	}
	if f.tags == nil {
		f.tags = map[string][]string{}
	}
	f.tags[target.SandboxID] = append(f.tags[target.SandboxID], request.Tag)
	return f.startErr
}

func testProvider(api apiClient) *provider {
	return &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
}

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func testOptions(t *testing.T, template string) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{"template": rawJSON(t, template)}
}

func testProvisioning(t *testing.T) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu, memoryMB := 2, 4096
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: testOptions(t, "claude"),
	}
}

func testPolicy(t *testing.T, config any, template string) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU, maxMemoryMB := 8, 8192
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, template)},
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: rawJSON(t, config),
	}
}

func (f *fakeAPI) ResumeSandbox(_ context.Context, id string) error {
	current := f.sandboxes[id]
	current.State = sandboxStateRunning
	f.sandboxes[id] = current
	return nil
}

func (f *fakeAPI) ListTemplates(context.Context) ([]template, error) {
	return f.templates, f.templateErr
}
