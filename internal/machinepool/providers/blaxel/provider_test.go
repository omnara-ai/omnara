package blaxel

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"path"
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
	if create.Metadata.Name != wantName ||
		create.Metadata.Labels["omnara-installation"] != testInstallationID().String() ||
		create.Metadata.Labels["omnara-machine"] != machineID.String() ||
		create.Spec.Region != "us-pdx-1" ||
		create.Spec.Runtime.Image != "blaxel/base-image:latest" ||
		create.Spec.Runtime.Memory != 1024 || len(create.Spec.Runtime.Ports) != 0 {
		t.Fatalf("unexpected create request: %+v", create)
	}
	if len(api.directoryCreates) != 0 || len(api.fileUploads) != 0 {
		t.Fatalf(
			"startup transport without startup script: directories=%v files=%+v",
			api.directoryCreates,
			api.fileUploads,
		)
	}
	if len(api.processRequests) != 1 {
		t.Fatalf("process requests = %d, want 1", len(api.processRequests))
	}
	process := api.processRequests[0]
	if process.Name != daemonProcessName || !process.KeepAlive ||
		process.Timeout != 0 || process.WaitForCompletion {
		t.Fatalf("unexpected process request: %+v", process)
	}
	if process.Command != providers.ManagedScopedBootScript("") {
		t.Fatalf("process command = %q, want daemon launcher", process.Command)
	}
	if process.Env["OMNARA_API_URL"] != "https://app.omnara.test" ||
		process.Env["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
		process.Env["OMNARA_STARTUP_ENV_FILE"] != "" ||
		process.Env["OMNARA_DAEMON_ENV_KEYS"] != "" ||
		process.Env[testStartupScriptEnvVar] != "" {
		t.Fatalf("unexpected daemon process env: %+v", process.Env)
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
	env := api.processRequests[0].Env
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
	if process.Command != providers.ManagedScopedBootScript(managedAwakeProcessSetupScript()) ||
		!strings.Contains(
			process.Command,
			"omnara_awake_process_name_prefix="+daemonprotocol.BlaxelAwakeProcessNamePrefix,
		) {
		t.Fatalf("daemon process is missing the awake process boot preamble: %q", process.Command)
	}
	processName := daemonprotocol.BlaxelAwakeProcessName(4242)
	awakeProcess, found := api.processesByName[result.ProviderResourceID+"/"+processName]
	if !found || processStatus(awakeProcess.Status) != processStatusRunning ||
		!awakeProcess.KeepAlive {
		t.Fatalf("initial awake process = %+v found=%t", awakeProcess, found)
	}
}

func TestBlaxelProviderStartupManifestIncludesSleepControls(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{
			"sleep_after_ms": 45000,
			"startup_script": "true",
		}),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision sleeping blaxel machine with startup: %v", err)
	}
	manifest := strings.Fields(api.processRequests[0].Env["OMNARA_DAEMON_ENV_KEYS"])
	for _, name := range []string{
		daemonprotocol.SleepAfterEnvVar,
		daemonprotocol.SleepPlatformEnvVar,
		daemonprotocol.WakeListenAddrEnvVar,
	} {
		if !slices.Contains(manifest, name) {
			t.Fatalf("daemon env manifest %v omitted sleep control %s", manifest, name)
		}
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

func TestBlaxelProviderProvisionIsolatesMachineEnvInStartupFile(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)

	_, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": "test \"$APP_ENV\" = production"}),
		"machine-token",
		map[string]string{"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret"},
	)
	if err != nil {
		t.Fatalf("provision blaxel machine: %v", err)
	}
	if len(api.fileUploads) != 1 {
		t.Fatalf("file uploads = %d, want 1", len(api.fileUploads))
	}
	if len(api.directoryCreates) != 1 ||
		api.directoryCreates[0] != path.Dir(api.fileUploads[0].path) {
		t.Fatalf("startup environment directories = %v", api.directoryCreates)
	}
	relativePath, hasPrefix := strings.CutPrefix(
		api.fileUploads[0].path,
		startupEnvironmentDirectoryPrefix,
	)
	attemptID, hasSuffix := strings.CutSuffix(relativePath, "/"+startupEnvironmentFileName)
	if !hasPrefix || !hasSuffix || len(attemptID) != startupEnvironmentRandomBytes*2 {
		t.Fatalf("startup environment path = %q", api.fileUploads[0].path)
	}
	if !strings.Contains(
		api.processRequests[0].Command,
		bootstrapAttemptMarkerPrefix+attemptID+"\n",
	) {
		t.Fatalf(
			"process command attempt marker does not match startup path %q",
			api.fileUploads[0].path,
		)
	}
	wantEnvironment, err := providers.RenderManagedStartupEnvironment(map[string]string{
		"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret",
	})
	if err != nil {
		t.Fatalf("render expected startup environment: %v", err)
	}
	if api.fileUploads[0].content != wantEnvironment {
		t.Fatalf("startup environment = %q, want %q", api.fileUploads[0].content, wantEnvironment)
	}
	processEnv := api.processRequests[0].Env
	if processEnv["APP_ENV"] != "" || processEnv["GITHUB_TOKEN"] != "" ||
		processEnv["OMNARA_API_URL"] == "" || processEnv["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
		processEnv["OMNARA_STARTUP_ENV_FILE"] != api.fileUploads[0].path {
		t.Fatalf("unexpected daemon process env: %+v", processEnv)
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
	if !providers.IsManagedScopedBootScript(process.Command) ||
		!strings.Contains(process.Command, bootstrapAttemptMarkerPrefix) {
		t.Fatalf("process command does not run startup before daemon")
	}
	wantPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	if process.Env[testStartupScriptEnvVar] != wantPayload {
		t.Fatalf("startup payload = %q, want %q", process.Env[testStartupScriptEnvVar], wantPayload)
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
			Labels: map[string]string{
				"omnara-installation": testInstallationID().String(),
				"omnara-machine":      machineID.String(),
			},
		},
		Status: "DEPLOYED",
	}
	api.sandboxesByName[name] = target
	api.processesByName[name+"/"+daemonProcessName] = sandboxProcess{
		Name: daemonProcessName, Command: providers.ManagedScopedBootScript(""),
		Status: processStatusRunning, KeepAlive: true,
	}

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"retry-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision existing blaxel machine: %v", err)
	}
	if result.ProviderResourceID != name || len(api.sandboxesByName) != 1 ||
		len(api.processRequests) != 0 || len(api.fileUploads) != 0 {
		t.Fatalf(
			"result=%q sandboxes=%d process requests=%d",
			result.ProviderResourceID,
			len(api.sandboxesByName),
			len(api.processRequests),
		)
	}
}

func TestBlaxelProviderProvisionRejectsIncompatibleExistingDaemon(t *testing.T) {
	api := newFakeAPI()
	provider := newTestProvider(api)
	machineID := uuid.New()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("blaxel sandbox name: %v", err)
	}
	api.sandboxesByName[name] = sandbox{
		Metadata: resourceMetadata{
			Name: name, URL: "https://sbx-existing.test.bl.run",
			Labels: map[string]string{
				"omnara-installation": testInstallationID().String(),
				"omnara-machine":      machineID.String(),
			},
		},
		Status: "DEPLOYED",
	}
	api.processesByName[name+"/"+daemonProcessName] = sandboxProcess{
		Name: daemonProcessName, Command: "legacy bootstrap",
		Status: processStatusRunning, KeepAlive: true,
	}

	_, err = provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"retry-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "incompatible bootstrap version") {
		t.Fatalf("provision error = %v, want incompatible bootstrap error", err)
	}
	if len(api.processRequests) != 0 {
		t.Fatalf("process requests = %d, want no unsafe adoption or replacement", len(api.processRequests))
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
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if !errors.Is(err, startErr) {
		t.Fatalf("provision error = %v, want %v", err, startErr)
	}
	if result.ProviderResourceID == "" {
		t.Fatal("daemon start error omitted the observed provider resource id")
	}
	if len(api.fileUploads) != 1 || !slices.Equal(api.deletedPaths, []string{
		api.fileUploads[0].path,
		path.Dir(api.fileUploads[0].path),
	}) {
		t.Fatalf("startup file cleanup uploads=%+v deletes=%v", api.fileUploads, api.deletedPaths)
	}
}

func TestBlaxelProviderCleanupOutlivesCanceledProvisionContext(t *testing.T) {
	api := newFakeAPI()
	startErr := errors.New("start failed")
	api.startProcessErr = startErr
	provider := newTestProvider(api)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.ProvisionMachine(
		ctx,
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if !errors.Is(err, startErr) {
		t.Fatalf("provision error = %v, want %v", err, startErr)
	}
	if len(api.deletedPathContextErrors) != 2 {
		t.Fatalf("cleanup context observations = %v, want two", api.deletedPathContextErrors)
	}
	for i, contextErr := range api.deletedPathContextErrors {
		if contextErr != nil {
			t.Fatalf("cleanup context %d error = %v, want live detached context", i, contextErr)
		}
	}
}

func TestBlaxelProviderProvisionRetainsFileWhenOwnStartWasAmbiguous(t *testing.T) {
	api := newFakeAPI()
	api.startProcessErr = errors.New("ambiguous start")
	api.createProcessOnStartError = true
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if err != nil {
		t.Fatalf("provision after ambiguous accepted start: %v", err)
	}
	if result.ProviderResourceID == "" || len(api.fileUploads) != 1 ||
		len(api.deletedPaths) != 0 {
		t.Fatalf(
			"result=%+v uploads=%+v deletes=%v",
			result,
			api.fileUploads,
			api.deletedPaths,
		)
	}
}

func TestBlaxelProviderProvisionCleansFileWhenConcurrentStartWins(t *testing.T) {
	api := newFakeAPI()
	api.startProcessErr = errors.New("concurrent start")
	api.createProcessOnStartError = true
	api.startErrorProcessCommand = providers.ManagedScopedBootScript("")
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if err != nil {
		t.Fatalf("provision after concurrent start: %v", err)
	}
	if result.ProviderResourceID == "" || len(api.fileUploads) != 1 ||
		!slices.Equal(api.deletedPaths, []string{
			api.fileUploads[0].path,
			path.Dir(api.fileUploads[0].path),
		}) {
		t.Fatalf(
			"result=%+v uploads=%+v deletes=%v",
			result,
			api.fileUploads,
			api.deletedPaths,
		)
	}
}

func TestBlaxelProviderProvisionRejectsIncompatibleProcessAfterStartError(t *testing.T) {
	api := newFakeAPI()
	api.startProcessErr = errors.New("ambiguous start")
	api.createProcessOnStartError = true
	api.startErrorProcessCommand = "legacy bootstrap"
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if err == nil || !strings.Contains(err.Error(), "incompatible bootstrap version") {
		t.Fatalf("provision error = %v, want incompatible bootstrap error", err)
	}
	if result.ProviderResourceID == "" || len(api.fileUploads) != 1 ||
		!slices.Equal(api.deletedPaths, []string{
			api.fileUploads[0].path,
			path.Dir(api.fileUploads[0].path),
		}) {
		t.Fatalf(
			"result=%+v uploads=%+v deletes=%v",
			result,
			api.fileUploads,
			api.deletedPaths,
		)
	}
}

func TestBlaxelProviderProvisionReturnsStartupEnvironmentUploadError(t *testing.T) {
	api := newFakeAPI()
	uploadErr := errors.New("upload failed")
	api.uploadFileErr = uploadErr
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if !errors.Is(err, uploadErr) {
		t.Fatalf("provision error = %v, want %v", err, uploadErr)
	}
	if result.ProviderResourceID == "" || len(api.processRequests) != 0 {
		t.Fatalf("result=%+v process requests=%d", result, len(api.processRequests))
	}
	if len(api.fileUploads) != 1 || !slices.Equal(api.deletedPaths, []string{
		api.fileUploads[0].path,
		path.Dir(api.fileUploads[0].path),
	}) {
		t.Fatalf("failed upload cleanup uploads=%+v deletes=%v", api.fileUploads, api.deletedPaths)
	}
}

func TestBlaxelProviderProvisionReturnsStartupEnvironmentDirectoryError(t *testing.T) {
	api := newFakeAPI()
	directoryErr := errors.New("create directory failed")
	api.createDirectoryErr = directoryErr
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if !errors.Is(err, directoryErr) {
		t.Fatalf("provision error = %v, want %v", err, directoryErr)
	}
	if result.ProviderResourceID == "" || len(api.directoryCreates) != 1 ||
		len(api.fileUploads) != 0 || len(api.processRequests) != 0 ||
		!slices.Equal(api.deletedPaths, []string{
			path.Join(api.directoryCreates[0], startupEnvironmentFileName),
			api.directoryCreates[0],
		}) {
		t.Fatalf(
			"result=%+v directories=%v uploads=%+v processes=%d deletes=%v",
			result,
			api.directoryCreates,
			api.fileUploads,
			len(api.processRequests),
			api.deletedPaths,
		)
	}
}

func TestWaitForInitialAwakeProcessUsesCappedBackoff(t *testing.T) {
	api := newFakeAPI()
	target := sandbox{Metadata: resourceMetadata{Name: "sandbox"}}
	name := daemonprotocol.BlaxelAwakeProcessName(4242)
	var delays []time.Duration
	err := waitForInitialAwakeProcessWithDelay(
		context.Background(),
		api,
		target,
		name,
		func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			if len(delays) == 6 {
				api.processesByName[target.Metadata.Name+"/"+name] = sandboxProcess{
					Status: processStatusRunning, KeepAlive: true,
				}
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("wait for initial awake process: %v", err)
	}
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}
	if !slices.Equal(delays, want) {
		t.Fatalf("poll delays = %v, want %v", delays, want)
	}
}

func TestWaitForInitialAwakeProcessHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForInitialAwakeProcessWithDelay(
		ctx,
		newFakeAPI(),
		sandbox{Metadata: resourceMetadata{Name: "sandbox"}},
		daemonprotocol.BlaxelAwakeProcessName(4242),
		waitForPollInterval,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context cancellation", err)
	}
}

func TestWaitForInitialAwakeProcessRejectsNonKeepingAliveProcess(t *testing.T) {
	api := newFakeAPI()
	target := sandbox{Metadata: resourceMetadata{Name: "sandbox"}}
	name := daemonprotocol.BlaxelAwakeProcessName(4242)
	api.processesByName[target.Metadata.Name+"/"+name] = sandboxProcess{
		Status: processStatusRunning,
	}
	err := waitForInitialAwakeProcessWithDelay(
		context.Background(),
		api,
		target,
		name,
		func(context.Context, time.Duration) error {
			t.Fatal("terminal awake process state unexpectedly polled again")
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not running with keep-alive enabled") {
		t.Fatalf("wait error = %v, want terminal readiness error", err)
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
		testMachineProvisioning(t, map[string]any{"startup_script": "echo ready"}),
		"machine-token",
		map[string]string{"SECRET": "resolved"},
	)
	if err == nil || !strings.Contains(err.Error(), `started with status "completed"`) {
		t.Fatalf("provision error = %v, want non-running daemon process error", err)
	}
	if result.ProviderResourceID == "" {
		t.Fatal("daemon status error omitted the observed provider resource id")
	}
	if len(api.fileUploads) != 1 || !slices.Equal(api.deletedPaths, []string{
		api.fileUploads[0].path,
		path.Dir(api.fileUploads[0].path),
	}) {
		t.Fatalf("startup file cleanup uploads=%+v deletes=%v", api.fileUploads, api.deletedPaths)
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
					Name: daemonProcessName, Command: providers.ManagedScopedBootScript(""),
					Status: processStatusRunning, KeepAlive: test.keepAlive,
				}
			} else {
				api.processKeepAlive = &test.keepAlive
			}

			_, err := ensureDaemonProcess(
				context.Background(), api, target, map[string]string{}, "/tmp/startup-env", "", test.sleepEnabled,
			)
			if err == nil || !strings.Contains(err.Error(), "keep-alive") {
				t.Fatalf("ensure daemon process error = %v, want keep-alive mismatch", err)
			}
			if test.existing {
				if len(api.directoryCreates) != 0 || len(api.fileUploads) != 0 ||
					len(api.deletedPaths) != 0 {
					t.Fatalf(
						"existing mismatch touched startup payload: directories=%v uploads=%+v deletes=%v",
						api.directoryCreates,
						api.fileUploads,
						api.deletedPaths,
					)
				}
				return
			}
			if len(api.fileUploads) != 1 || !slices.Equal(api.deletedPaths, []string{
				api.fileUploads[0].path,
				path.Dir(api.fileUploads[0].path),
			}) {
				t.Fatalf(
					"started mismatch cleanup uploads=%+v deletes=%v",
					api.fileUploads,
					api.deletedPaths,
				)
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
					Labels: map[string]string{
						"omnara-installation": testInstallationID().String(),
						"omnara-machine":      machineID.String(),
					},
				},
				Status: status,
			}

			_, err = provider.ProvisionMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testMachineProvisioning(t, nil),
				"machine-token",
				nil,
			)
			if err == nil || !strings.Contains(err.Error(), "was deleted; provisioning must be retried") {
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
		{name: "terminating", status: "TERMINATING"},
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
					Labels: map[string]string{
						"omnara-installation": testInstallationID().String(),
						"omnara-machine":      machineID.String(),
					},
				},
				Status: test.status,
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
						"omnara-installation": testInstallationID().String(),
						"omnara-machine":      "other",
					},
				},
				Status: status,
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
	installationOwner := testInstallationID().String()
	machineOwner := machineID.String()
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
		{"terminating", "TERMINATING", installationOwner, machineOwner, true, true, false},
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
					"omnara-installation": test.installationOwner,
					"omnara-machine":      test.machineOwner,
				}
				api.sandboxesByName[name] = sandbox{
					Metadata: resourceMetadata{Name: name, Labels: labels},
					Status:   test.status,
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
				"omnara-installation": installationOwner,
				"omnara-machine":      machineOwner,
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
				"omnara-installation": installationOwner,
				"omnara-machine":      machineOwner,
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
			"omnara-installation": installationOwner,
			"omnara-machine":      machineOwner,
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
}

func newTestProvider(api apiClient) *provider {
	return &provider{
		api:             api,
		workspace:       "omnara",
		apiToken:        "api-token",
		omnaraPublicURL: "https://app.omnara.test",
	}
}

type fakeAPI struct {
	sandboxesByName           map[string]sandbox
	processesByName           map[string]sandboxProcess
	createRequests            []createSandboxRequest
	processRequests           []processRequest
	directoryCreates          []string
	fileUploads               []fakeFileUpload
	deletedPaths              []string
	deletedPathContextErrors  []error
	wakeTarget                sandbox
	deletedNames              []string
	createErr                 error
	startProcessErr           error
	createProcessOnStartError bool
	startErrorProcessCommand  string
	createDirectoryErr        error
	uploadFileErr             error
	wakeErr                   error
	processStatus             string
	processKeepAlive          *bool
}

type fakeFileUpload struct {
	path    string
	content string
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
			[]string{"FAILED", "TERMINATED", "TERMINATING"},
			sandboxStatus(existing.Status),
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

func (f *fakeAPI) UploadSandboxFile(
	_ context.Context,
	_ sandbox,
	path string,
	content string,
) error {
	f.fileUploads = append(f.fileUploads, fakeFileUpload{path: path, content: content})
	return f.uploadFileErr
}

func (f *fakeAPI) CreateSandboxDirectory(
	_ context.Context,
	_ sandbox,
	path string,
) error {
	f.directoryCreates = append(f.directoryCreates, path)
	return f.createDirectoryErr
}

func (f *fakeAPI) DeleteSandboxPath(
	ctx context.Context,
	_ sandbox,
	path string,
) error {
	f.deletedPaths = append(f.deletedPaths, path)
	f.deletedPathContextErrors = append(f.deletedPathContextErrors, ctx.Err())
	return nil
}

func (f *fakeAPI) StartSandboxProcess(
	_ context.Context,
	target sandbox,
	request processRequest,
) (sandboxProcess, error) {
	f.processRequests = append(f.processRequests, request)
	if f.startProcessErr != nil {
		if f.createProcessOnStartError {
			command := request.Command
			if f.startErrorProcessCommand != "" {
				command = f.startErrorProcessCommand
			}
			f.processesByName[target.Metadata.Name+"/"+request.Name] = sandboxProcess{
				PID: "4242", Name: request.Name, Command: command,
				Status: processStatusRunning, KeepAlive: request.KeepAlive,
			}
		}
		return sandboxProcess{}, f.startProcessErr
	}
	key := target.Metadata.Name + "/" + request.Name
	keepAlive := request.KeepAlive
	if f.processKeepAlive != nil {
		keepAlive = *f.processKeepAlive
	}
	process := sandboxProcess{
		PID:       "4242",
		Name:      request.Name,
		Command:   request.Command,
		Status:    f.processStatus,
		KeepAlive: keepAlive,
	}
	f.processesByName[key] = process
	if request.Name == daemonProcessName && !request.KeepAlive &&
		processStatus(process.Status) == processStatusRunning {
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
