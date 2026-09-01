package unikraft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestUnikraftProviderProvisionCreatesDisposableInstance(t *testing.T) {
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision unikraft machine: %v", err)
	}
	wantName := mustInstanceName(t, machineID)
	wantResourceID := "uuid-" + wantName
	if result.ProviderResourceID != wantResourceID {
		t.Fatalf("provider resource id = %q, want UUID %q", result.ProviderResourceID, wantResourceID)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
	create := api.createRequests[0]
	if create.Name != wantName || create.Image != "registry.example/daemon:latest" || create.MemoryMB != 1024 ||
		create.VCPUs != 1 ||
		!create.Autostart ||
		create.RestartPolicy != "never" {
		t.Fatalf("unexpected create request: %+v", create)
	}
	for _, key := range []string{
		"OMNARA_API_URL",
		"OMNARA_INSTALLER_URL",
		"OMNARA_MACHINE_TOKEN",
		providers.ManagedBootstrapScriptEnvVar,
	} {
		if create.Env[key] == "" {
			t.Fatalf("missing bootstrap env %s in %+v", key, create.Env)
		}
	}
	for _, key := range []string{
		"OMNARA_HOME",
		"OMNARA_DAEMON_PATH",
		"OMNARA_DAEMON_RELEASE_URL",
		"OMNARA_DAEMON_SEED_PATH",
	} {
		if _, ok := create.Env[key]; ok {
			t.Fatalf("provider should not supply %s: %+v", key, create.Env)
		}
	}
	if !reflect.DeepEqual(create.Args, providers.ManagedDaemonLauncherArgs()) {
		t.Fatalf("create args = %#v, want %#v", create.Args, providers.ManagedDaemonLauncherArgs())
	}
	bootstrapScript := decodeManagedBootstrapScript(t, create.Env[providers.ManagedBootstrapScriptEnvVar])
	for _, value := range []string{`"${OMNARA_INSTALLER_URL:?}"`, "--install-only", "start --no-service"} {
		if !strings.Contains(bootstrapScript, value) {
			t.Fatalf("bootstrap script missing %q", value)
		}
	}
	if _, ok := create.Env["OMNARA_INSTALLATION_ID"]; ok {
		t.Fatalf("installation id should not be supplied to daemon: %+v", create.Env)
	}
	if _, ok := create.Env["OMNARA_MACHINE_ID"]; ok {
		t.Fatalf("machine id should not be supplied to daemon: %+v", create.Env)
	}
	if _, ok := create.Env["APP_ENV"]; ok {
		t.Fatalf("provisioning env includes execution env: %+v", create.Env)
	}
	if _, ok := create.Env[testStartupScriptEnvVar]; ok {
		t.Fatalf("startup script env should be omitted without startup_script: %+v", create.Env)
	}
}

func TestUnikraftProviderProvisionTrimsImage(t *testing.T) {
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineID := uuid.New()

	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{
				"provider_options": map[string]any{
					"image": " registry.example/daemon:latest ",
				},
			},
		),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision unikraft machine: %v", err)
	}
	if got := api.createRequests[0].Image; got != "registry.example/daemon:latest" {
		t.Fatalf("create image = %q, want trimmed image", got)
	}
}

func TestUnikraftProviderProvisionIncludesStartupScriptOrchestration(t *testing.T) {
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineID := uuid.New()
	startupScript := "echo startup-ready\n"

	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{"provider_options": map[string]any{"startup_script": "echo startup-ready\n"}},
		),
		"opaque-machine-token",
		map[string]string{"USER_TOKEN": strings.Repeat("b", 256)},
	)
	if err != nil {
		t.Fatalf("provision unikraft machine: %v", err)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
	create := api.createRequests[0]
	if got := create.Env[testStartupScriptEnvVar]; got != base64.StdEncoding.EncodeToString([]byte(startupScript)) {
		t.Fatalf("startup script env = %q, want encoded script", got)
	}
	if !reflect.DeepEqual(create.Args, providers.ManagedDaemonLauncherArgs()) {
		t.Fatalf("create args = %#v, want %#v", create.Args, providers.ManagedDaemonLauncherArgs())
	}
	bootstrapScript := decodeManagedBootstrapScript(t, create.Env[providers.ManagedBootstrapScriptEnvVar])
	if !strings.Contains(bootstrapScript, testStartupScriptEnvVar) ||
		!strings.Contains(bootstrapScript, `"${OMNARA_INSTALLER_URL:?}"`) {
		t.Fatalf("bootstrap script does not include startup orchestration and daemon launcher")
	}
	if size := unikraftApplicationCommandLineBytes(create.Args, create.Env); size > 2560 {
		t.Fatalf("managed application command line is %d bytes, want at most 2560", size)
	}
}

func unikraftApplicationCommandLineBytes(args []string, env map[string]string) int {
	size := len("env.vars=[ ] -- ")
	for key, value := range env {
		size += len(key) + len(value) + 4
	}
	for _, arg := range args {
		size += len(arg) + 1
	}
	return size
}

func TestUnikraftProviderProvisionIncludesMachineEnv(t *testing.T) {
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineID := uuid.New()

	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"machine-token",
		map[string]string{"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret"},
	)
	if err != nil {
		t.Fatalf("provision unikraft machine: %v", err)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
	create := api.createRequests[0]
	if create.Env["APP_ENV"] != "production" || create.Env["GITHUB_TOKEN"] != "resolved-secret" {
		t.Fatalf("create env missing machine env: %+v", create.Env)
	}
	if create.Env["OMNARA_API_URL"] == "" || create.Env["OMNARA_MACHINE_TOKEN"] != "machine-token" {
		t.Fatalf("create env missing bootstrap env: %+v", create.Env)
	}
}

func TestUnikraftProviderAcceptsFutureMetroSlug(t *testing.T) {
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineID := uuid.New()
	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{"provider_options": map[string]any{"metro": "future-us-1"}},
		),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("expected future metro slug to pass local validation: %v", err)
	}
}

func TestUnikraftProviderRejectsInvalidMetroSlug(t *testing.T) {
	provider := newTestProvider(&fakeAPI{instancesByName: map[string]instance{}})
	machineID := uuid.New()
	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{"provider_options": map[string]any{"metro": "bad/metro"}},
		),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "valid DNS label") {
		t.Fatalf("expected invalid metro slug error, got %v", err)
	}
}

func TestUnikraftProviderRequiresMetro(t *testing.T) {
	provider := newTestProvider(&fakeAPI{instancesByName: map[string]instance{}})
	machineID := uuid.New()
	cpu := 1
	memoryMB := 1024
	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		executionstore.MachineProvisioningConfig{
			CPU:      &cpu,
			MemoryMB: &memoryMB,
			ProviderOptions: map[string]json.RawMessage{
				"image": json.RawMessage(`"registry.example/daemon:latest"`),
			},
		},
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "requires metro") {
		t.Fatalf("expected missing metro error, got %v", err)
	}
}

func TestUnikraftProviderRejectsRestartPolicyProviderOption(t *testing.T) {
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineID := uuid.New()
	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{"provider_options": map[string]any{"restart_policy": "never"}},
		),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), `unknown field "restart_policy"`) {
		t.Fatalf("expected restart_policy to be rejected as a config field, got %v", err)
	}
}

func TestUnikraftProviderRejectsUserArgsUntilStartupScriptsAreSupported(t *testing.T) {
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineID := uuid.New()
	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{"provider_options": map[string]any{"args": []string{"echo", "user"}}},
		),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), `unknown field "args"`) {
		t.Fatalf("expected args to be rejected, got %v", err)
	}
}

func TestUnikraftProviderRejectsInvalidStartupScriptShape(t *testing.T) {
	provider := newTestProvider(&fakeAPI{instancesByName: map[string]instance{}})
	machineID := uuid.New()
	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{"provider_options": map[string]any{"startup_script": []string{"echo", "bad"}}},
		),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal array") {
		t.Fatalf("expected non-string startup_script error, got %v", err)
	}

	oversized := strings.Repeat("x", 64*1024+1)
	_, err = provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(
			t,
			map[string]any{"provider_options": map[string]any{"startup_script": oversized}},
		),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "startup_script must be at most") {
		t.Fatalf("expected startup_script size error, got %v", err)
	}
}

func TestUnikraftProviderProvisionRecoversFromCreateErrorWhenInstanceExists(t *testing.T) {
	machineID := uuid.New()
	name := mustInstanceName(t, machineID)
	api := &fakeAPI{
		instancesByName:             map[string]instance{},
		createErr:                   errors.New("request timed out after create"),
		createErrStillCreatesRecord: true,
	}
	provider := newTestProvider(api)
	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision should recover by inspecting existing instance: %v", err)
	}
	wantResourceID := "uuid-" + name
	if result.ProviderResourceID != wantResourceID {
		t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, wantResourceID)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
}

func TestUnikraftProviderProvisionRetriesDoNotCreateCompetingInstances(t *testing.T) {
	machineID := uuid.New()
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)
	machineProvisioning := testMachineProvisioning(t, nil)

	first, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		machineProvisioning,
		"machine-token-a",
		nil,
	)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	second, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		machineProvisioning,
		"machine-token-b",
		nil,
	)
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if first.ProviderResourceID == "" || first.ProviderResourceID != second.ProviderResourceID {
		t.Fatalf(
			"provider resource ids first=%q second=%q, want same non-empty id",
			first.ProviderResourceID,
			second.ProviderResourceID,
		)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
	if got := api.createRequests[0].Env["OMNARA_MACHINE_TOKEN"]; got != "machine-token-a" {
		t.Fatalf("created instance token = %q, want first bootstrap token", got)
	}
}

func TestUnikraftProviderInspectMachineByUUIDAndMachineID(t *testing.T) {
	machineID := uuid.New()
	name := mustInstanceName(t, machineID)
	api := &fakeAPI{instancesByName: map[string]instance{name: {UUID: "uuid-1", Name: name}}}
	provider := &provider{api: api, omnara: providers.ManagedMachineEndpoints{
		APIURL:       "https://api.omnara.test/v1",
		InstallerURL: "https://app.omnara.test/install/omnarad.sh",
	}}
	machineProvisioning := testMachineProvisioning(t, nil)

	byUUID, found, err := provider.InspectMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		machineProvisioning,
		"uuid-1",
	)
	if err != nil {
		t.Fatalf("inspect by uuid: %v", err)
	}
	if !found || byUUID != "uuid-1" {
		t.Fatalf("inspect by uuid found=%v resourceID=%q, want found with uuid", found, byUUID)
	}

	byMachineID, found, err := provider.InspectMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		machineProvisioning,
		"",
	)
	if err != nil {
		t.Fatalf("inspect by machine id: %v", err)
	}
	if !found || byMachineID != "uuid-1" {
		t.Fatalf("inspect by machine id found=%v resourceID=%q, want found with uuid", found, byMachineID)
	}
}

func TestUnikraftProviderInspectMachineByUUIDRejectsMissingUUID(t *testing.T) {
	machineID := uuid.New()
	api := &fakeAPI{instancesByUUID: map[string]instance{"uuid-1": {Name: "bad"}}}
	provider := &provider{api: api, omnara: providers.ManagedMachineEndpoints{
		APIURL:       "https://api.omnara.test/v1",
		InstallerURL: "https://app.omnara.test/install/omnarad.sh",
	}}

	_, _, err := provider.InspectMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"uuid-1",
	)
	if err == nil || !strings.Contains(err.Error(), "missing instance uuid") {
		t.Fatalf("expected missing uuid error, got %v", err)
	}
}

func TestUnikraftProviderDeleteByUUID(t *testing.T) {
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{instancesByUUID: map[string]instance{
		"uuid-1":       {UUID: "uuid-1", Name: name},
		"uuid-foreign": {UUID: "uuid-foreign", Name: "other"},
	}}
	provider := &provider{api: api, omnara: providers.ManagedMachineEndpoints{
		APIURL:       "https://api.omnara.test/v1",
		InstallerURL: "https://app.omnara.test/install/omnarad.sh",
	}}
	err = provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"uuid-1",
	)
	if err != nil {
		t.Fatalf("delete by uuid: %v", err)
	}
	if len(api.deletedUUIDs) != 1 || api.deletedUUIDs[0] != "uuid-1" {
		t.Fatalf("delete by uuid deleted=%+v, want uuid-1", api.deletedUUIDs)
	}
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"uuid-foreign",
	); err == nil || !strings.Contains(err.Error(), "expected allocation name") {
		t.Fatalf("foreign machine error = %v, want ownership error", err)
	}
	if len(api.deletedUUIDs) != 1 {
		t.Fatalf("deleted foreign instance: %v", api.deletedUUIDs)
	}
	api.instancesByName = map[string]instance{name: {UUID: "uuid-current", Name: name}}
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"uuid-stale",
	); err != nil {
		t.Fatalf("delete by allocation name: %v", err)
	}
	if len(api.deletedUUIDs) != 2 || api.deletedUUIDs[1] != "uuid-current" {
		t.Fatalf("deleted instances: %v", api.deletedUUIDs)
	}
	delete(api.instancesByName, name)
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"uuid-already-absent",
	); err != nil {
		t.Fatalf("delete already absent machine: %v", err)
	}
	if len(api.deletedUUIDs) != 2 {
		t.Fatalf("already absent machine caused a delete request: %v", api.deletedUUIDs)
	}
}

func TestUnikraftProviderDeleteUsesOnlyImmutableMetroFromStoredProvisioning(t *testing.T) {
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{instancesByUUID: map[string]instance{
		"uuid-existing": {UUID: "uuid-existing", Name: name},
	}}
	provider := &provider{api: api, omnara: providers.ManagedMachineEndpoints{
		APIURL:       "https://api.omnara.test/v1",
		InstallerURL: "https://app.omnara.test/install/omnarad.sh",
	}}
	machineProvisioning := testMachineProvisioning(t, nil)
	machineProvisioning.CPU = nil
	machineProvisioning.MemoryMB = nil
	machineProvisioning.ProviderOptions["image"] = json.RawMessage(`{"stored":"shape"}`)
	machineProvisioning.ProviderOptions["startup_script"] = json.RawMessage(`["stored"]`)
	machineProvisioning.ProviderOptions["removed_option"] = json.RawMessage(`true`)

	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		machineProvisioning,
		"uuid-existing",
	); err != nil {
		t.Fatalf("delete machine with stored provisioning: %v", err)
	}
	if len(api.deletedUUIDs) != 1 || api.deletedUUIDs[0] != "uuid-existing" {
		t.Fatalf("deleted instances = %v, want uuid-existing", api.deletedUUIDs)
	}
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api: api,
		omnara: providers.ManagedMachineEndpoints{
			APIURL:       "https://api.omnara.test/v1",
			InstallerURL: "https://app.omnara.test/install/omnarad.sh",
		},
	}
}

type fakeAPI struct {
	instancesByName             map[string]instance
	instancesByUUID             map[string]instance
	batchGetRequests            [][]string
	batchGetResults             []instance
	batchGetErr                 error
	batchGetMaxSize             int
	batchEnvelopeStatus         responseStatus
	batchHasEnvelopeErrors      bool
	getByUUIDRequests           []string
	getByUUIDErr                error
	createRequests              []createInstanceRequest
	deletedUUIDs                []string
	createErr                   error
	createErrStillCreatesRecord bool
}

func (f *fakeAPI) CreateInstance(
	_ context.Context,
	req createInstanceRequest,
) (instance, error) {
	f.createRequests = append(f.createRequests, req)
	if f.createErr != nil {
		if f.createErrStillCreatesRecord {
			f.instancesByName[req.Name] = instance{UUID: "uuid-" + req.Name, Name: req.Name}
		}
		return instance{}, f.createErr
	}
	created := instance{UUID: "uuid-" + req.Name, Name: req.Name}
	f.instancesByName[req.Name] = created
	return created, nil
}

func (f *fakeAPI) GetInstancesByUUIDs(
	_ context.Context,
	uuids []string,
) (instanceBatch, error) {
	f.batchGetRequests = append(f.batchGetRequests, append([]string(nil), uuids...))
	if f.batchGetMaxSize > 0 && len(uuids) > f.batchGetMaxSize {
		return instanceBatch{}, providers.ErrResponseTooLarge
	}
	if f.batchGetErr != nil {
		return instanceBatch{}, f.batchGetErr
	}
	envelopeStatus := f.batchEnvelopeStatus
	if envelopeStatus == "" {
		envelopeStatus = responseStatusSuccess
	}
	if f.batchGetResults != nil {
		return instanceBatch{
			Instances:         append([]instance(nil), f.batchGetResults...),
			EnvelopeStatus:    envelopeStatus,
			HasEnvelopeErrors: f.batchHasEnvelopeErrors,
		}, nil
	}
	results := make([]instance, 0, len(uuids))
	for _, uuid := range uuids {
		result, ok := f.instancesByUUID[uuid]
		if !ok {
			for _, candidate := range f.instancesByName {
				if candidate.UUID == uuid {
					result, ok = candidate, true
					break
				}
			}
		}
		if ok {
			results = append(results, result)
		}
	}
	return instanceBatch{
		Instances:         results,
		EnvelopeStatus:    envelopeStatus,
		HasEnvelopeErrors: f.batchHasEnvelopeErrors,
	}, nil
}

func (f *fakeAPI) GetInstanceByUUID(_ context.Context, uuid string) (instance, bool, error) {
	f.getByUUIDRequests = append(f.getByUUIDRequests, uuid)
	if f.getByUUIDErr != nil {
		return instance{}, false, f.getByUUIDErr
	}
	if f.instancesByUUID != nil {
		result, ok := f.instancesByUUID[uuid]
		return result, ok, nil
	}
	for _, result := range f.instancesByName {
		if result.UUID == uuid {
			return result, true, nil
		}
	}
	return instance{}, false, nil
}

func (f *fakeAPI) GetInstanceByName(_ context.Context, name string) (instance, bool, error) {
	result, ok := f.instancesByName[name]
	return result, ok, nil
}

func (f *fakeAPI) DeleteInstanceByUUID(_ context.Context, uuid string) error {
	f.deletedUUIDs = append(f.deletedUUIDs, uuid)
	return nil
}
