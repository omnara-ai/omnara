package superserve

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
	return executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, templateName, startupScript)}
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
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			ProviderOptions: testOptions(t, templateName, ""),
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

func mustOwnershipMetadata(t *testing.T, installationID, machineID uuid.UUID) map[string]string {
	t.Helper()
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		t.Fatalf("ownership metadata: %v", err)
	}
	return metadata
}

func ownedSandbox(t *testing.T, id string, status sandboxStatus, installationID, machineID uuid.UUID) sandbox {
	t.Helper()
	return sandbox{
		ID:        id,
		Name:      "omnara-mch-test",
		Status:    status,
		VCPUCount: 1,
		MemoryMiB: 1024,
		Metadata:  mustOwnershipMetadata(t, installationID, machineID),
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
	templates       []template
	templatesErr    error
	templateQueries []string

	createRequest    createSandboxRequest
	createCalls      int
	createErr        error
	createErrCreates bool
	createNoToken    bool
	// created is the sandbox the fake owns: create returns it, and get and
	// activate fall back to it.
	created sandbox

	sandboxes []sandbox
	listErr   error
	listCalls int

	getResults   []sandbox
	getFound     bool
	getErrs      []error
	getLookups   []string
	getDeadlines []time.Duration

	activateResults []sandbox
	activateErrs    []error
	activateCalls   int
	activateTokens  int

	deleteCalls int
	deletedID   string
	deleteErr   error

	execCalls    int
	execSessions []execSession
	execRequests []execRequest
	execErrs     []error
	execResults  []execResult
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		templates: []template{{
			ID:        "tpl-base",
			Name:      defaultTemplate,
			Status:    templateStatusReady,
			VCPU:      1,
			MemoryMiB: 1024,
		}},
		created: sandbox{
			ID:        "sb-123",
			Status:    sandboxStatusActive,
			VCPUCount: 1,
			MemoryMiB: 1024,
		},
		getFound: true,
	}
}

func (a *fakeAPI) ListTemplates(_ context.Context, query string) ([]template, error) {
	a.templateQueries = append(a.templateQueries, query)
	return a.templates, a.templatesErr
}

func (a *fakeAPI) CreateSandbox(_ context.Context, request createSandboxRequest) (sandbox, error) {
	a.createCalls++
	a.createRequest = request
	if a.created.Name == "" {
		a.created.Name = request.Name
	}
	if a.created.Metadata == nil {
		a.created.Metadata = request.Metadata
	}
	if a.createErr != nil {
		if a.createErrCreates {
			a.sandboxes = append(a.sandboxes, a.created)
		}
		return sandbox{}, a.createErr
	}
	created := a.created
	if !a.createNoToken {
		created.AccessToken = "create-token"
		created.RoutingHint = "create-hint"
	}
	return created, nil
}

func (a *fakeAPI) ListSandboxes(_ context.Context, metadata map[string]string) ([]sandbox, error) {
	a.listCalls++
	if a.listErr != nil {
		return nil, a.listErr
	}
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

func (a *fakeAPI) ActivateSandbox(_ context.Context, id string) (sandbox, error) {
	a.activateCalls++
	if len(a.activateErrs) > 0 {
		err := a.activateErrs[0]
		a.activateErrs = a.activateErrs[1:]
		return sandbox{}, err
	}
	a.activateTokens++
	token := "token-" + string(rune('0'+a.activateTokens))
	if len(a.activateResults) > 0 {
		next := a.activateResults[0]
		a.activateResults = a.activateResults[1:]
		next.AccessToken = token
		return next, nil
	}
	activated := a.created
	activated.ID = id
	activated.Status = sandboxStatusActive
	activated.AccessToken = token
	activated.RoutingHint = "hint-" + token
	return activated, nil
}

func (a *fakeAPI) DeleteSandbox(_ context.Context, id string) error {
	a.deleteCalls++
	a.deletedID = id
	return a.deleteErr
}

func (a *fakeAPI) Exec(_ context.Context, session execSession, request execRequest) (execResult, error) {
	a.execCalls++
	a.execSessions = append(a.execSessions, session)
	a.execRequests = append(a.execRequests, request)
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
