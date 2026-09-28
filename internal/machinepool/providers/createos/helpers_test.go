package createos

import (
	"context"
	"encoding/json"
	"testing"

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

func testOptions(t *testing.T, shape, rootfs, region, startupScript string) map[string]json.RawMessage {
	t.Helper()
	return map[string]json.RawMessage{
		"shape":          mustRawJSON(t, shape),
		"rootfs":         mustRawJSON(t, rootfs),
		"region":         mustRawJSON(t, region),
		"startup_script": mustRawJSON(t, startupScript),
	}
}

func testMachineProvisioning(
	t *testing.T,
	shape, rootfs, region, startupScript string,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	return executionstore.MachineProvisioningConfig{
		ProviderOptions: testOptions(t, shape, rootfs, region, startupScript),
	}
}

func testPoolPolicy(
	t *testing.T,
	config json.RawMessage,
	shape, rootfs, region string,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 16384
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			ProviderOptions: testOptions(t, shape, rootfs, region, ""),
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

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:          api,
		omnaraAPIURL: "https://api.omnara.test/v1",
	}
}

// sandboxPage is one response from the paginated sandbox listing.
type sandboxPage struct {
	items []sandbox
	total int
}

type fakeAPI struct {
	shapes      []Shape
	shapesErr   error
	shapeCalls  int
	rootfs      RootFSCatalog
	rootfsErr   error
	rootfsCalls int

	createRequest createSandboxRequest
	createCalls   int
	createErr     error
	created       sandbox

	listPages   []sandboxPage
	listErr     error
	listQueries [][2]int

	getResults []sandbox
	getFound   bool
	getErr     error
	getLookups []string

	deleteCalls int
	deletedID   string
	deleteErr   error

	processes     []process
	processErr    error
	processCalls  int
	processTarget string

	createdProcess       process
	createProcessCalls   int
	createProcessErr     error
	createProcessRequest createProcessRequest
	createProcessTarget  string
}

func newFakeAPI() *fakeAPI {
	created := sandbox{
		ID:     "sb-123",
		Status: sandboxStatusRunning,
		Shape:  "s-1vcpu-1gb",
		RootFS: "devbox:1",
		Region: "us",
	}
	return &fakeAPI{
		shapes:         []Shape{{ID: "s-1vcpu-1gb", VCPU: 1, MemMiB: 1024}},
		rootfs:         RootFSCatalog{Names: []string{"devbox:1"}, Default: "devbox:1"},
		created:        created,
		getFound:       true,
		createdProcess: process{ID: "proc-1", State: "running"},
	}
}

func (a *fakeAPI) ListShapes(context.Context) ([]Shape, error) {
	a.shapeCalls++
	return a.shapes, a.shapesErr
}

func (a *fakeAPI) ListRootFS(context.Context) (RootFSCatalog, error) {
	a.rootfsCalls++
	return a.rootfs, a.rootfsErr
}

func (a *fakeAPI) CreateSandbox(_ context.Context, request createSandboxRequest) (sandbox, error) {
	a.createCalls++
	a.createRequest = request
	if a.createErr != nil {
		return sandbox{}, a.createErr
	}
	if a.created.Name == "" {
		a.created.Name = request.Name
	}
	return a.created, nil
}

func (a *fakeAPI) ListSandboxes(_ context.Context, limit, offset int) ([]sandbox, int, error) {
	a.listQueries = append(a.listQueries, [2]int{limit, offset})
	if a.listErr != nil {
		return nil, 0, a.listErr
	}
	if len(a.listPages) == 0 {
		return nil, 0, nil
	}
	page := a.listPages[0]
	a.listPages = a.listPages[1:]
	return page.items, page.total, nil
}

func (a *fakeAPI) GetSandbox(_ context.Context, id string) (sandbox, bool, error) {
	a.getLookups = append(a.getLookups, id)
	if a.getErr != nil {
		return sandbox{}, false, a.getErr
	}
	if len(a.getResults) > 0 {
		next := a.getResults[0]
		a.getResults = a.getResults[1:]
		return next, a.getFound, nil
	}
	target := a.created
	if target.Name == "" {
		target.Name = a.createRequest.Name
	}
	return target, a.getFound, nil
}

func (a *fakeAPI) DeleteSandbox(_ context.Context, id string) error {
	a.deleteCalls++
	a.deletedID = id
	return a.deleteErr
}

func (a *fakeAPI) ListProcesses(_ context.Context, id string) ([]process, error) {
	a.processCalls++
	a.processTarget = id
	return a.processes, a.processErr
}

func (a *fakeAPI) CreateProcess(_ context.Context, id string, request createProcessRequest) (process, error) {
	a.createProcessCalls++
	a.createProcessTarget = id
	a.createProcessRequest = request
	if a.createProcessErr != nil {
		return process{}, a.createProcessErr
	}
	return a.createdProcess, nil
}

var _ apiClient = (*fakeAPI)(nil)
