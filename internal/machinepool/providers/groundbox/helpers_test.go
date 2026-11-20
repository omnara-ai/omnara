package groundbox

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

var (
	testInstallationID = uuid.MustParse("6f1c2b8e-3d4a-4f5b-9c6d-7e8f9a0b1c2d")
	testMachineID      = uuid.MustParse("0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d")
)

type fakeAPI struct {
	sandboxes   map[string]sandbox
	creates     []createSandboxRequest
	createKeys  []string
	createErr   error
	createState sandboxStatus
	listErr     error
	getErr      error
	getCalls    int

	resumes   []string
	starts    []string
	resumeErr error
	deleted   []string

	commands     map[string][]command
	commandReqs  []startCommandRequest
	commandErr   error
	listCmdCalls int
}

func (f *fakeAPI) CreateSandbox(_ context.Context, key string, request createSandboxRequest) (sandbox, error) {
	f.creates = append(f.creates, request)
	f.createKeys = append(f.createKeys, key)
	if f.createErr != nil {
		return sandbox{}, f.createErr
	}
	status := f.createState
	if status == "" {
		status = sandboxStatusRunning
	}
	id := fmt.Sprintf("sbx_test%04d", len(f.creates))
	return f.add(id, status, request.CPUs, request.MemoryGB, request.Metadata), nil
}

func (f *fakeAPI) add(id string, status sandboxStatus, cpus, memoryGB int, metadata map[string]string) sandbox {
	if f.sandboxes == nil {
		f.sandboxes = map[string]sandbox{}
	}
	created := sandbox{ID: id, Status: status, CPUs: cpus, MemoryGB: memoryGB, Metadata: metadata}
	f.sandboxes[id] = created
	return created
}

func (f *fakeAPI) ListSandboxes(_ context.Context, metadata map[string]string) ([]sandbox, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var found []sandbox
	for _, current := range f.sandboxes {
		if sandboxEnded(current.Status) {
			continue
		}
		matches := true
		for key, value := range metadata {
			matches = matches && current.Metadata[key] == value
		}
		if matches {
			found = append(found, current)
		}
	}
	return found, nil
}

func (f *fakeAPI) GetSandbox(_ context.Context, id string) (sandbox, bool, error) {
	f.getCalls++
	if f.getErr != nil {
		return sandbox{}, false, f.getErr
	}
	current, found := f.sandboxes[id]
	return current, found, nil
}

func (f *fakeAPI) ResumeSandbox(_ context.Context, id string) (sandbox, error) {
	f.resumes = append(f.resumes, id)
	if f.resumeErr != nil {
		return sandbox{}, f.resumeErr
	}
	return f.setStatus(id, sandboxStatusRunning), nil
}

func (f *fakeAPI) StartSandbox(_ context.Context, id string) (sandbox, error) {
	f.starts = append(f.starts, id)
	return f.setStatus(id, sandboxStatusRunning), nil
}

func (f *fakeAPI) setStatus(id string, status sandboxStatus) sandbox {
	current := f.sandboxes[id]
	current.Status = status
	f.sandboxes[id] = current
	return current
}

func (f *fakeAPI) DeleteSandbox(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	if _, found := f.sandboxes[id]; found {
		f.setStatus(id, sandboxStatusStopped)
	}
	return nil
}

func (f *fakeAPI) ListRunningCommands(_ context.Context, id string) ([]command, error) {
	f.listCmdCalls++
	return f.commands[id], nil
}

func (f *fakeAPI) StartCommand(_ context.Context, id string, request startCommandRequest) (command, error) {
	f.commandReqs = append(f.commandReqs, request)
	if f.commandErr != nil {
		return command{}, f.commandErr
	}
	if f.commands == nil {
		f.commands = map[string][]command{}
	}
	started := command{ID: fmt.Sprintf("ex_%016d", len(f.commandReqs)), Cmd: request.Cmd, State: commandStateRunning}
	f.commands[id] = append(f.commands[id], started)
	return started, nil
}

func testProvider(api apiClient) *provider {
	return &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func testProvisioning(t *testing.T, options map[string]any) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu, memoryMB := 2, 4096
	raw := map[string]json.RawMessage{}
	for key, value := range options {
		raw[key] = mustRawJSON(t, value)
	}
	return executionstore.MachineProvisioningConfig{CPU: &cpu, MemoryMB: &memoryMB, ProviderOptions: raw}
}

func testPolicy(t *testing.T, config any, options map[string]any) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU, maxMemoryMB := 8, 16384
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: testProvisioning(t, options),
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: mustRawJSON(t, config),
	}
}

func ownedSandbox(t *testing.T, api *fakeAPI, id string, status sandboxStatus) sandbox {
	t.Helper()
	metadata, err := ownershipMetadata(testInstallationID, testMachineID)
	if err != nil {
		t.Fatalf("ownership metadata: %v", err)
	}
	return api.add(id, status, 2, 4, metadata)
}

func statusError(status int, code string) error {
	return apiError{StatusCode: status, Code: code}
}
