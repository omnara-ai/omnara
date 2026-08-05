package daytona

import (
	"context"
	"encoding/json"
	"testing"

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

func testOptions(t *testing.T, snapshot, target, startupScript string) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{
		"snapshot":       mustRawJSON(t, snapshot),
		"target":         mustRawJSON(t, target),
		"startup_script": mustRawJSON(t, startupScript),
	}
}

func testMachineProvisioning(
	t *testing.T,
	snapshot, target, startupScript string,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu := 2
	memoryMB := 4096
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: testOptions(t, snapshot, target, startupScript),
	}
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:             api,
		omnaraPublicURL: "https://app.omnara.test",
	}
}

type fakeAPI struct {
	snapshot           snapshot
	snapshotErr        error
	sandbox            sandbox
	sandboxStates      []string
	getSandboxCalls    int
	createErr          error
	createRequest      createSandboxRequest
	session            session
	sessionFound       bool
	createSessionErr   error
	createSessionCalls int
	deleteSessionCalls int
	executeCalls       int
	executedSession    string
	executeRequest     sessionExecuteRequest
	deleteCalls        int
	deletedResourceID  string
	missingSandboxIDs  map[string]bool
}

func (a *fakeAPI) GetSnapshot(context.Context, string) (snapshot, error) {
	return a.snapshot, a.snapshotErr
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		sandbox: sandbox{
			ID:              "sandbox-123",
			Target:          "us",
			CPU:             2,
			Memory:          4,
			State:           "started",
			ToolboxProxyURL: "https://proxy.app.daytona.io/toolbox",
		},
	}
}

func (a *fakeAPI) CreateSandbox(
	_ context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	a.createRequest = request
	if a.sandbox.Name == "" {
		a.sandbox.Name = request.Name
	}
	if a.sandbox.Labels == nil {
		a.sandbox.Labels = map[string]string{"omnara-machine": request.Name}
	}
	if a.createErr != nil {
		return sandbox{}, a.createErr
	}
	return a.sandbox, nil
}

func (a *fakeAPI) GetSandbox(_ context.Context, resourceID string) (sandbox, bool, error) {
	a.getSandboxCalls++
	if a.missingSandboxIDs[resourceID] {
		return sandbox{}, false, nil
	}
	if len(a.sandboxStates) > 0 {
		a.sandbox.State = a.sandboxStates[0]
		a.sandboxStates = a.sandboxStates[1:]
	}
	return a.sandbox, true, nil
}

func (a *fakeAPI) DeleteSandbox(_ context.Context, resourceID string) error {
	a.deleteCalls++
	a.deletedResourceID = resourceID
	return nil
}

func (a *fakeAPI) CreateSession(context.Context, sandbox, string) error {
	a.createSessionCalls++
	return a.createSessionErr
}

func (a *fakeAPI) GetSession(
	context.Context,
	sandbox,
	string,
) (session, bool, error) {
	return a.session, a.sessionFound, nil
}

func (a *fakeAPI) DeleteSession(context.Context, sandbox, string) error {
	a.deleteSessionCalls++
	a.sessionFound = false
	return nil
}

func (a *fakeAPI) ExecuteSessionCommand(
	_ context.Context,
	_ sandbox,
	sessionID string,
	request sessionExecuteRequest,
) (sessionExecuteResponse, error) {
	a.executeCalls++
	a.executedSession = sessionID
	a.executeRequest = request
	return sessionExecuteResponse{CommandID: "cmd-1"}, nil
}

var _ apiClient = (*fakeAPI)(nil)
