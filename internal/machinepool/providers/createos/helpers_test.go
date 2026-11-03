package createos

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

func testOptions(t *testing.T, shape, rootfs, startupScript string) map[string]json.RawMessage {
	t.Helper()
	options := map[string]json.RawMessage{
		"shape":  mustRawJSON(t, shape),
		"rootfs": mustRawJSON(t, rootfs),
	}
	if startupScript != "" {
		options["startup_script"] = mustRawJSON(t, startupScript)
	}
	return options
}

func testMachineProvisioning(
	t *testing.T,
	shape, rootfs, startupScript string,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	return executionstore.MachineProvisioningConfig{
		ProviderOptions: testOptions(t, shape, rootfs, startupScript),
	}
}

func testPoolPolicy(
	t *testing.T,
	config json.RawMessage,
	shape, rootfs string,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 16384
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			ProviderOptions: testOptions(t, shape, rootfs, ""),
		},
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: config,
	}
}

func mustAllocationName(t *testing.T, installationID, machineID uuid.UUID) string {
	t.Helper()
	name, err := allocationName(installationID, machineID)
	if err != nil {
		t.Fatalf("allocation name: %v", err)
	}
	return name
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:          api,
		omnaraAPIURL: "https://api.omnara.test/v1",
		pollDelay:    time.Millisecond,
	}
}

type listQuery struct {
	status        sandboxStatus
	limit, offset int
}

type fakeAPI struct {
	shapes     []sandboxShape
	shapesErr  error
	shapeCalls int

	createRequest    createSandboxRequest
	createCalls      int
	createErr        error
	createErrCreates bool
	created          sandbox

	sandboxes   []sandbox
	pageLimit   int
	listQueries []listQuery

	getResults   []sandbox
	getFound     bool
	getErrs      []error
	getLookups   []string
	getDeadlines []time.Duration

	deleteCalls int
	deletedID   string

	resumeCalls int
	resumeErr   error

	processes []process

	createProcessCalls   int
	createProcessRequest commandRequest
	createProcessTarget  string

	execCalls   int
	execTarget  string
	execRequest commandRequest
	execErrs    []error
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		shapes: []sandboxShape{{ID: "s-1vcpu-1gb", VCPU: 1, MemMiB: 1024}},
		created: sandbox{
			ID:     "sb-123",
			Status: sandboxStatusRunning,
			Shape:  "s-1vcpu-1gb",
		},
		getFound: true,
	}
}

func (a *fakeAPI) ListShapes(context.Context) ([]sandboxShape, error) {
	a.shapeCalls++
	return a.shapes, a.shapesErr
}

func (a *fakeAPI) CreateSandbox(_ context.Context, request createSandboxRequest) (sandbox, error) {
	a.createCalls++
	a.createRequest = request
	if a.created.Name == "" {
		a.created.Name = request.Name
	}
	if a.createErr != nil {
		if a.createErrCreates {
			a.sandboxes = append(a.sandboxes, a.created)
		}
		return sandbox{}, a.createErr
	}
	return sandbox{ID: a.created.ID, Name: a.created.Name}, nil
}

func (a *fakeAPI) ListSandboxes(
	_ context.Context,
	status sandboxStatus,
	limit, offset int,
) ([]sandbox, int, error) {
	a.listQueries = append(a.listQueries, listQuery{status: status, limit: limit, offset: offset})
	if a.pageLimit > 0 {
		limit = min(limit, a.pageLimit)
	}
	var matching []sandbox
	for _, item := range a.sandboxes {
		if item.Status == status {
			matching = append(matching, item)
		}
	}
	if offset >= len(matching) {
		return nil, len(matching), nil
	}
	return matching[offset:min(offset+limit, len(matching))], len(matching), nil
}

func (a *fakeAPI) GetSandbox(ctx context.Context, id string) (sandbox, bool, error) {
	a.getLookups = append(a.getLookups, id)
	if deadline, ok := ctx.Deadline(); ok {
		a.getDeadlines = append(a.getDeadlines, time.Until(deadline))
	}
	if len(a.getErrs) > 0 {
		err := a.getErrs[0]
		a.getErrs = a.getErrs[1:]
		return sandbox{}, false, err
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

func (a *fakeAPI) ResumeSandbox(context.Context, string) error {
	a.resumeCalls++
	return a.resumeErr
}

func (a *fakeAPI) ListProcesses(context.Context, string) ([]process, error) {
	return a.processes, nil
}

func (a *fakeAPI) CreateProcess(_ context.Context, id string, request commandRequest) (process, error) {
	a.createProcessCalls++
	a.createProcessTarget = id
	a.createProcessRequest = request
	return process{ID: "proc-1", State: processStateRunning}, nil
}

func (a *fakeAPI) Exec(_ context.Context, id string, request commandRequest) error {
	a.execCalls++
	a.execTarget = id
	a.execRequest = request
	if len(a.execErrs) > 0 {
		err := a.execErrs[0]
		a.execErrs = a.execErrs[1:]
		return err
	}
	return nil
}

var _ apiClient = (*fakeAPI)(nil)
