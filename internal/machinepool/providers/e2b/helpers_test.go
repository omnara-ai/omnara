package e2b

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type fakeAPI struct {
	templatePages [][]template
	repeatCursor  bool
	templateErr   error
	templateCalls int
	teamAliases   map[string]string
	resolveErr    error

	sandboxes map[string]sandbox
	creates   []createSandboxRequest
	createErr error
	hide      bool
	findErr   error

	extended      []string
	starts        []startProcessRequest
	startErr      error
	startUnlisted bool
	tags          map[string][]string
	deleted       []string
}

func (f *fakeAPI) ListTemplates(_ context.Context, _ string, cursor string) ([]template, string, error) {
	f.templateCalls++
	if f.templateErr != nil {
		return nil, "", f.templateErr
	}
	page := 0
	if cursor != "" {
		page, _ = strconv.Atoi(strings.TrimPrefix(cursor, "page-"))
	}
	next := ""
	if f.repeatCursor {
		next = "page-1"
	} else if page+1 < len(f.templatePages) {
		next = "page-" + strconv.Itoa(page+1)
	}
	return f.templatePages[page], next, nil
}

func (f *fakeAPI) ResolveTemplateAlias(_ context.Context, alias string) (string, error) {
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	if templateID, ok := f.teamAliases[alias]; ok {
		return templateID, nil
	}
	for _, page := range f.templatePages {
		for _, current := range page {
			if current.IsDefault && (slices.Contains(current.Names, alias) || current.TemplateID == alias) {
				return "", apiError{StatusCode: http.StatusForbidden}
			}
		}
	}
	return "", apiError{StatusCode: http.StatusNotFound}
}

func (f *fakeAPI) CreateSandbox(_ context.Context, request createSandboxRequest) (sandbox, error) {
	f.creates = append(f.creates, request)
	if f.createErr != nil {
		return sandbox{}, f.createErr
	}
	created := f.addSandbox(fmt.Sprintf("sbx%d", len(f.creates)), request.Metadata, 2, 2048)
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

func (f *fakeAPI) ExtendTimeout(_ context.Context, id string, _ int) error {
	f.extended = append(f.extended, id)
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
	cpu, memoryMB := 2, 2048
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
