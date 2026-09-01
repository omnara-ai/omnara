package blaxel

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestBlaxelProviderProvisionCreatesSandboxAndDaemon(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	machineID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	machineProvisioning := testMachineProvisioning(t, nil)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		machineProvisioning,
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision blaxel machine: %v", err)
	}
	wantName, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("blaxel sandbox name: %v", err)
	}
	if result.ProviderResourceID != wantName {
		t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, wantName)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
	create := api.createRequests[0]
	wantLabels := mustSandboxOwnershipLabels(t, testInstallationID(), machineID)
	if create.Metadata.Name != wantName ||
		create.Metadata.Labels[installationLabel] != wantLabels[installationLabel] ||
		create.Metadata.Labels[machineLabel] != wantLabels[machineLabel] ||
		!strings.HasPrefix(create.Metadata.Labels[installationLabel], "inst_") ||
		!strings.HasPrefix(create.Metadata.Labels[machineLabel], "mch_") ||
		create.Spec.Region != "us-pdx-1" ||
		create.Spec.Runtime.Image != "blaxel/base-image:latest" ||
		create.Spec.Runtime.Memory != 1024 || len(create.Spec.Runtime.Ports) != 0 {
		t.Fatalf("unexpected create request: %+v", create)
	}
	env := sandboxEnvMap(create.Spec.Runtime.Envs)
	if len(env) != 3 ||
		env["OMNARA_API_URL"] != "https://api.omnara.test/v1" ||
		env["OMNARA_INSTALLER_URL"] != "https://app.omnara.test/install/omnarad.sh" ||
		env["OMNARA_MACHINE_TOKEN"] != "machine-token" {
		t.Fatalf("unexpected sandbox env: %+v", create.Spec.Runtime.Envs)
	}
	if _, ok := env[testStartupScriptEnvVar]; ok {
		t.Fatalf("startup payload should be omitted: %+v", create.Spec.Runtime.Envs)
	}
	if len(api.processRequests) != 1 {
		t.Fatalf("process requests = %d, want 1", len(api.processRequests))
	}
	process := api.processRequests[0]
	if process.Name != daemonProcessName || !process.KeepAlive ||
		process.Timeout != 0 || process.WaitForCompletion {
		t.Fatalf("unexpected process request: %+v", process)
	}
	if process.Command != providers.ManagedBootScript() {
		t.Fatalf("process command = %q, want daemon launcher", process.Command)
	}
}

func TestBlaxelProviderProvisionEnablesSleepWithInitialAwakeProcess(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"sleep_after_ms": 45000}),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision sleeping blaxel machine: %v", err)
	}
	if result.SandboxURL == "" {
		t.Fatal("sleeping blaxel machine omitted sandbox url")
	}
	if provider.ProvisioningTimeout() != 15*time.Second {
		t.Fatalf("provisioning timeout = %s", provider.ProvisioningTimeout())
	}
	env := sandboxEnvMap(api.createRequests[0].Spec.Runtime.Envs)
	if env[daemonprotocol.SleepAfterEnvVar] != "45000" ||
		env[daemonprotocol.WakeListenAddrEnvVar] != ":"+
			strconv.Itoa(daemonprotocol.WakeListenerPort) ||
		env[daemonprotocol.SleepPlatformEnvVar] != daemonprotocol.SleepPlatformBlaxel {
		t.Fatalf("unexpected sleep env: %+v", env)
	}
	ports := api.createRequests[0].Spec.Runtime.Ports
	if len(ports) != 1 || ports[0].Name != wakePortName ||
		ports[0].Protocol != wakePortProtocol ||
		ports[0].Target != daemonprotocol.WakeListenerPort {
		t.Fatalf("unexpected sleep ports: %+v", ports)
	}
	if len(api.processRequests) != 1 {
		t.Fatalf("process requests = %d, want 1", len(api.processRequests))
	}
	process := api.processRequests[0]
	if process.Name != daemonProcessName || process.KeepAlive || process.Timeout != 0 ||
		process.WaitForCompletion {
		t.Fatalf("unexpected daemon process: %+v", process)
	}
	managedBoot := providers.ManagedBootScript()
	if !strings.HasSuffix(process.Command, managedBoot) ||
		!strings.Contains(
			process.Command,
			"omnara_awake_process_name_prefix="+daemonprotocol.BlaxelAwakeProcessNamePrefix,
		) {
		t.Fatalf("daemon process is missing the awake process boot preamble: %q", process.Command)
	}
	processName := daemonprotocol.BlaxelAwakeProcessName(4242)
	awakeProcess, found := api.processesByName[result.ProviderResourceID+"/"+processName]
	if !found || normalizeSandboxProcessStatus(awakeProcess.Status) != processStatusRunning ||
		!awakeProcess.KeepAlive {
		t.Fatalf("initial awake process = %+v found=%t", awakeProcess, found)
	}
}

func TestBlaxelProviderWakeUsesSandboxPort(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	input := providers.WakeMachineInput{
		ProviderResourceID: "omnara-mch-test",
		SandboxURL:         "https://sbx-test.bl.run",
	}
	if err := provider.WakeMachine(context.Background(), input); err != nil {
		t.Fatalf("wake blaxel machine: %v", err)
	}
	if api.wakeTarget.Metadata.Name != input.ProviderResourceID ||
		api.wakeTarget.Metadata.URL != input.SandboxURL {
		t.Fatalf("unexpected wake target: %+v", api.wakeTarget)
	}

	wakeErr := errors.New("wake failed")
	api.wakeErr = wakeErr
	if err := provider.WakeMachine(context.Background(), input); !errors.Is(err, wakeErr) {
		t.Fatalf("failed wake error = %v, want %v", err, wakeErr)
	}
}

func TestBlaxelProviderProvisionIncludesMachineEnv(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)

	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, nil),
		"machine-token",
		map[string]string{"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret"},
	)
	if err != nil {
		t.Fatalf("provision blaxel machine: %v", err)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
	env := sandboxEnvMap(api.createRequests[0].Spec.Runtime.Envs)
	if env["APP_ENV"] != "production" || env["GITHUB_TOKEN"] != "resolved-secret" {
		t.Fatalf("sandbox env missing machine env: %+v", env)
	}
	if env["OMNARA_API_URL"] == "" || env["OMNARA_MACHINE_TOKEN"] != "machine-token" {
		t.Fatalf("sandbox env missing bootstrap env: %+v", env)
	}
}

func TestBlaxelProviderProvisionRunsStartupScriptBeforeDaemon(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	startupScript := "echo startup-ready\n"

	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": startupScript}),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision blaxel machine: %v", err)
	}
	process := api.processRequests[0]
	wantCommand := providers.ManagedBootScript()
	if process.Command != wantCommand {
		t.Fatalf("process command does not run startup before daemon")
	}
	env := sandboxEnvMap(api.createRequests[0].Spec.Runtime.Envs)
	wantPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	if env[testStartupScriptEnvVar] != wantPayload {
		t.Fatalf("startup payload = %q, want %q", env[testStartupScriptEnvVar], wantPayload)
	}
}

func TestBlaxelProviderProvisionRetryConvergesOnExistingSandbox(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("blaxel sandbox name: %v", err)
	}
	target := sandbox{
		Metadata: resourceMetadata{
			Name: name, URL: "https://sbx-existing.test.bl.run",
			Labels: mustSandboxOwnershipLabels(t, testInstallationID(), machineID),
		},
		Status: "DEPLOYED",
	}
	api.sandboxesByName[name] = target
	api.processesByName[name+"/"+daemonProcessName] = sandboxProcess{
		Status: processStatusRunning, KeepAlive: true,
	}

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"retry-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision existing blaxel machine: %v", err)
	}
	if result.ProviderResourceID != name || len(api.sandboxesByName) != 1 ||
		len(api.processRequests) != 0 {
		t.Fatalf(
			"result=%q sandboxes=%d process requests=%d",
			result.ProviderResourceID,
			len(api.sandboxesByName),
			len(api.processRequests),
		)
	}
}

func TestBlaxelProviderProvisionRejectsMissingCreateConflict(t *testing.T) {
	api := newFakeAPI()
	api.createErr = apiError{StatusCode: http.StatusConflict}
	provider := newTestProvider(api)

	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, nil),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "create conflicted but the sandbox is not usable") {
		t.Fatalf("provision error = %v, want unusable conflict error", err)
	}
	if len(api.createRequests) != 1 || len(api.processRequests) != 0 || len(api.deletedNames) != 0 {
		t.Fatalf(
			"creates=%d processes=%d deleted=%v",
			len(api.createRequests),
			len(api.processRequests),
			api.deletedNames,
		)
	}
}

func TestBlaxelProviderProvisionReturnsDaemonStartError(t *testing.T) {
	api := newFakeAPI()
	startErr := errors.New("start failed")
	api.startProcessErr = startErr
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, nil),
		"machine-token",
		nil,
	)
	if !errors.Is(err, startErr) {
		t.Fatalf("provision error = %v, want %v", err, startErr)
	}
	if result.ProviderResourceID == "" {
		t.Fatal("daemon start error omitted the observed provider resource id")
	}
}

func TestBlaxelProviderProvisionRejectsNonRunningDaemonProcess(t *testing.T) {
	api := newFakeAPI()
	api.processStatus = "completed"
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, nil),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), `started with status "completed"`) {
		t.Fatalf("provision error = %v, want non-running daemon process error", err)
	}
	if result.ProviderResourceID == "" {
		t.Fatal("daemon status error omitted the observed provider resource id")
	}
}

func TestEnsureDaemonProcessRejectsMismatchedKeepAlive(t *testing.T) {
	tests := []struct {
		name         string
		sleepEnabled bool
		keepAlive    bool
		existing     bool
	}{
		{name: "existing with sleep disabled", keepAlive: false, existing: true},
		{name: "existing with sleep enabled", sleepEnabled: true, keepAlive: true, existing: true},
		{name: "started with sleep disabled", keepAlive: false},
		{name: "started with sleep enabled", sleepEnabled: true, keepAlive: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := newFakeAPI()
			target := sandbox{Metadata: resourceMetadata{Name: "sandbox"}}
			if test.existing {
				api.processesByName["sandbox/"+daemonProcessName] = sandboxProcess{
					Status: processStatusRunning, KeepAlive: test.keepAlive,
				}
			} else {
				api.processKeepAlive = &test.keepAlive
			}

			_, err := ensureDaemonProcess(
				context.Background(), api, target, "", test.sleepEnabled,
			)
			if err == nil || !strings.Contains(err.Error(), "keep-alive") {
				t.Fatalf("ensure daemon process error = %v, want keep-alive mismatch", err)
			}
		})
	}
}

func TestBlaxelProviderProvisionReplacesUnusableSandbox(t *testing.T) {
	for _, status := range []string{"FAILED", "TERMINATED", "DEACTIVATED"} {
		t.Run(status, func(t *testing.T) {
			api := newFakeAPI()
			provider := newTestProvider(api)
			machineID := uuid.New()
			name, err := providers.MachineAllocationName(testInstallationID(), machineID)
			if err != nil {
				t.Fatalf("blaxel sandbox name: %v", err)
			}
			api.sandboxesByName[name] = sandbox{
				Metadata: resourceMetadata{
					Name: name, URL: "https://sbx-old.test.bl.run",
					Labels: mustSandboxOwnershipLabels(t, testInstallationID(), machineID),
				},
				Status: sandboxDeploymentStatus(status),
			}

			_, err = provider.ProvisionMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testMachineProvisioning(t, nil),
				"machine-token",
				nil,
			)
			if !errors.Is(err, providers.ErrResourceReplaced) {
				t.Fatalf("first provision after %s conflict error = %v", status, err)
			}
			if len(api.deletedNames) != 1 || api.deletedNames[0] != name ||
				len(api.createRequests) != 1 || len(api.processRequests) != 0 {
				t.Fatalf(
					"deleted=%v creates=%d processes=%d",
					api.deletedNames,
					len(api.createRequests),
					len(api.processRequests),
				)
			}

			result, err := provider.ProvisionMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testMachineProvisioning(t, nil),
				"machine-token",
				nil,
			)
			if err != nil {
				t.Fatalf("retry provision after %s conflict: %v", status, err)
			}
			if result.ProviderResourceID != name || len(api.deletedNames) != 1 ||
				len(api.createRequests) != 2 || len(api.processRequests) != 1 {
				t.Fatalf(
					"result=%q deleted=%v creates=%d processes=%d",
					result.ProviderResourceID,
					api.deletedNames,
					len(api.createRequests),
					len(api.processRequests),
				)
			}
		})
	}
}

func TestBlaxelProviderProvisionRejectsNonReadySandbox(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
	}{
		{name: "uploading", status: "UPLOADING"},
		{name: "building", status: "BUILDING"},
		{name: "built", status: "BUILT"},
		{name: "deploying", status: "DEPLOYING"},
		{name: "deleting", status: "DELETING"},
		{name: "deactivating", status: "DEACTIVATING"},
		{name: "empty", status: ""},
		{name: "unknown", status: "UNKNOWN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newFakeAPI()
			provider := newTestProvider(api)
			machineID := uuid.New()
			name, err := providers.MachineAllocationName(testInstallationID(), machineID)
			if err != nil {
				t.Fatalf("blaxel sandbox name: %v", err)
			}
			api.sandboxesByName[name] = sandbox{
				Metadata: resourceMetadata{
					Name: name, URL: "https://sbx-old.test.bl.run",
					Labels: mustSandboxOwnershipLabels(t, testInstallationID(), machineID),
				},
				Status: sandboxDeploymentStatus(test.status),
			}

			_, err = provider.ProvisionMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testMachineProvisioning(t, nil),
				"machine-token",
				nil,
			)
			if err == nil || !strings.Contains(err.Error(), "not ready") {
				t.Fatalf("provision error = %v, want non-ready sandbox error", err)
			}
			if len(api.deletedNames) != 0 || len(api.processRequests) != 0 {
				t.Fatalf("deleted=%v process requests=%d", api.deletedNames, len(api.processRequests))
			}
		})
	}
}

func TestBlaxelProviderProvisionRejectsNameCollision(t *testing.T) {
	for _, status := range []string{"DEPLOYED", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			api := newFakeAPI()
			provider := newTestProvider(api)
			machineID := uuid.New()
			name, err := providers.MachineAllocationName(testInstallationID(), machineID)
			if err != nil {
				t.Fatalf("blaxel sandbox name: %v", err)
			}
			api.sandboxesByName[name] = sandbox{
				Metadata: resourceMetadata{
					Name: name, URL: "https://sbx-collision.test.bl.run",
					Labels: map[string]string{
						installationLabel: mustSandboxOwnershipLabels(
							t, testInstallationID(), machineID,
						)[installationLabel],
						machineLabel: "other",
					},
				},
				Status: sandboxDeploymentStatus(status),
			}

			_, err = provider.ProvisionMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testMachineProvisioning(t, nil),
				"machine-token",
				nil,
			)
			if err == nil || !strings.Contains(err.Error(), "ownership label") {
				t.Fatalf("provision collision error = %v", err)
			}
			if len(api.deletedNames) != 0 || len(api.processRequests) != 0 {
				t.Fatalf("deleted=%v process requests=%d", api.deletedNames, len(api.processRequests))
			}
		})
	}
}

func TestBlaxelProviderInspectAndDelete(t *testing.T) {
	machineID := uuid.New()
	owners := mustSandboxOwnershipLabels(t, testInstallationID(), machineID)
	installationOwner := owners[installationLabel]
	machineOwner := owners[machineLabel]
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("blaxel sandbox name: %v", err)
	}
	for _, test := range []struct {
		name              string
		status            string
		installationOwner string
		machineOwner      string
		exists            bool
		found             bool
		wantErr           bool
	}{
		{"deployed", "DEPLOYED", installationOwner, machineOwner, true, true, false},
		{"failed", "FAILED", installationOwner, machineOwner, true, true, false},
		{"deactivating", "DEACTIVATING", installationOwner, machineOwner, true, true, false},
		{"terminated", "TERMINATED", installationOwner, machineOwner, true, true, false},
		{"missing", "", "", "", false, false, false},
		{"missing ownership labels", "DEPLOYED", "", "", true, false, true},
		{"foreign installation label", "DEPLOYED", "other", machineOwner, true, false, true},
		{"foreign machine label", "DEPLOYED", installationOwner, "other", true, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newFakeAPI()
			if test.exists {
				labels := map[string]string{
					installationLabel: test.installationOwner,
					machineLabel:      test.machineOwner,
				}
				api.sandboxesByName[name] = sandbox{
					Metadata: resourceMetadata{Name: name, Labels: labels},
					Status:   sandboxDeploymentStatus(test.status),
				}
			}
			provider := newTestProvider(api)
			resourceID, found, err := provider.InspectMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testMachineProvisioning(t, nil),
				"",
			)
			if (err != nil) != test.wantErr || found != test.found {
				t.Fatalf("inspect resource=%q found=%t err=%v", resourceID, found, err)
			}
			wantResourceID := ""
			if test.found {
				wantResourceID = name
			}
			if resourceID != wantResourceID {
				t.Fatalf("resource id = %q, want %q", resourceID, wantResourceID)
			}
		})
	}

	api := newFakeAPI()
	api.sandboxesByName[name] = sandbox{
		Metadata: resourceMetadata{
			Name: name,
			Labels: map[string]string{
				installationLabel: installationOwner,
				machineLabel:      machineOwner,
			},
		},
	}
	provider := newTestProvider(api)
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		executionstore.MachineProvisioningConfig{},
		"",
	); err == nil {
		t.Fatal("expected missing provider resource id error")
	}
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		executionstore.MachineProvisioningConfig{},
		name,
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	if len(api.deletedNames) != 1 || api.deletedNames[0] != name {
		t.Fatalf("deleted names = %v", api.deletedNames)
	}
	foreignName := "foreign"
	api.sandboxesByName[foreignName] = sandbox{
		Metadata: resourceMetadata{
			Name: foreignName,
			Labels: map[string]string{
				installationLabel: installationOwner,
				machineLabel:      machineOwner,
			},
		},
	}
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		executionstore.MachineProvisioningConfig{},
		foreignName,
	); err == nil || !strings.Contains(err.Error(), "ownership labels") {
		t.Fatalf("foreign machine error = %v, want ownership error", err)
	}
	if len(api.deletedNames) != 1 {
		t.Fatalf("deleted foreign sandbox: %v", api.deletedNames)
	}
	api.sandboxesByName[name] = sandbox{Metadata: resourceMetadata{
		Name: name,
		Labels: map[string]string{
			installationLabel: installationOwner,
			machineLabel:      machineOwner,
		},
	}}
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		executionstore.MachineProvisioningConfig{},
		"stale",
	); err != nil {
		t.Fatalf("delete machine by allocation name: %v", err)
	}
	if len(api.deletedNames) != 2 || api.deletedNames[1] != name {
		t.Fatalf("deleted names = %v", api.deletedNames)
	}
	if err := provider.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		executionstore.MachineProvisioningConfig{},
		"already-absent",
	); err != nil {
		t.Fatalf("delete already absent machine: %v", err)
	}
	if len(api.deletedNames) != 2 {
		t.Fatalf("already absent machine caused a delete request: %v", api.deletedNames)
	}
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:       api,
		workspace: "omnara",
		apiToken:  "api-token",
		omnara: providers.ManagedMachineEndpoints{
			APIURL:       "https://api.omnara.test/v1",
			InstallerURL: "https://app.omnara.test/install/omnarad.sh",
		},
	}
}

type fakeAPI struct {
	sandboxesByName  map[string]sandbox
	processesByName  map[string]sandboxProcess
	createRequests   []createSandboxRequest
	processRequests  []processRequest
	wakeTarget       sandbox
	deletedNames     []string
	createErr        error
	startProcessErr  error
	wakeErr          error
	processStatus    sandboxProcessStatus
	processKeepAlive *bool
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		sandboxesByName: map[string]sandbox{},
		processesByName: map[string]sandboxProcess{},
		processStatus:   processStatusRunning,
	}
}

func (f *fakeAPI) CreateSandbox(
	_ context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	f.createRequests = append(f.createRequests, request)
	if f.createErr != nil {
		return sandbox{}, f.createErr
	}
	if existing, exists := f.sandboxesByName[request.Metadata.Name]; exists {
		if slices.Contains(
			[]sandboxDeploymentStatus{
				sandboxDeploymentFailed,
				sandboxDeploymentTerminated,
				sandboxDeploymentDeactivating,
			},
			normalizeSandboxDeploymentStatus(existing.Status),
		) {
			return sandbox{}, apiError{StatusCode: http.StatusConflict}
		}
		return existing, nil
	}
	created := sandbox{
		Metadata: resourceMetadata{
			Name: request.Metadata.Name, Labels: request.Metadata.Labels,
			URL: "https://sbx-" + request.Metadata.Name + ".test.bl.run",
		},
		Status: "DEPLOYED",
	}
	f.sandboxesByName[request.Metadata.Name] = created
	return created, nil
}

func (f *fakeAPI) GetSandbox(
	_ context.Context,
	name string,
) (sandbox, bool, error) {
	target, found := f.sandboxesByName[name]
	return target, found, nil
}

func (f *fakeAPI) DeleteSandbox(_ context.Context, name string) error {
	f.deletedNames = append(f.deletedNames, name)
	delete(f.sandboxesByName, name)
	return nil
}

func (f *fakeAPI) StartSandboxProcess(
	_ context.Context,
	target sandbox,
	request processRequest,
) (sandboxProcess, error) {
	f.processRequests = append(f.processRequests, request)
	if f.startProcessErr != nil {
		return sandboxProcess{}, f.startProcessErr
	}
	key := target.Metadata.Name + "/" + request.Name
	keepAlive := request.KeepAlive
	if f.processKeepAlive != nil {
		keepAlive = *f.processKeepAlive
	}
	process := sandboxProcess{
		PID:       "4242",
		Status:    f.processStatus,
		KeepAlive: keepAlive,
	}
	f.processesByName[key] = process
	if request.Name == daemonProcessName && !request.KeepAlive &&
		normalizeSandboxProcessStatus(process.Status) == processStatusRunning {
		processName := daemonprotocol.BlaxelAwakeProcessName(4242)
		f.processesByName[target.Metadata.Name+"/"+processName] = sandboxProcess{
			PID: "4243", Status: processStatusRunning, KeepAlive: true,
		}
	}
	return process, nil
}

func (f *fakeAPI) GetSandboxProcess(
	_ context.Context,
	target sandbox,
	name string,
) (sandboxProcess, bool, error) {
	process, found := f.processesByName[target.Metadata.Name+"/"+name]
	return process, found, nil
}

func (f *fakeAPI) WakeSandbox(_ context.Context, target sandbox) error {
	f.wakeTarget = target
	return f.wakeErr
}
