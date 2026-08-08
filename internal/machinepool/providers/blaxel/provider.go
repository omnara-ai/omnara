package blaxel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	daemonProcessName               = "omnara-daemon"
	wakePortName                    = "omnara-wake"
	wakePortProtocol                = "HTTP"
	processStatusRunning            = "running"
	provisioningTimeout             = 15 * time.Second
	initialAwakeProcessPollInterval = 100 * time.Millisecond
	maxAwakeProcessPollInterval     = time.Second
	// Blaxel's filesystem write follows symlinks and does not repair modes on
	// existing paths. Make the final directory component unpredictable beneath
	// the trusted root home, then create the fixed-name file inside it.
	startupEnvironmentDirectoryPrefix = "/root/.omnara-bootstrap-"
	startupEnvironmentFileName        = "startup-env"
	bootstrapAttemptMarkerPrefix      = "# omnara-blaxel-bootstrap-attempt:"
	startupEnvironmentRandomBytes     = 16
	startupEnvironmentCleanupTimeout  = 3 * time.Second
)

type provider struct {
	api             apiClient
	workspace       string
	apiToken        string
	omnaraPublicURL string
}

func (p *provider) apiClient() apiClient {
	if p.api != nil {
		return p.api
	}
	return &restClient{
		apiBaseURL: apiBaseURL,
		workspace:  p.workspace,
		apiToken:   p.apiToken,
		httpClient: providers.NewHTTPClient(),
	}
}

func (*provider) ProvisioningTimeout() time.Duration {
	return provisioningTimeout
}

func (*provider) PrepareProvisioning(
	_ context.Context,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	if _, err := providerOptionsFromProvisioning(machineProvisioning); err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	return executionstore.MachineResourceFacts{
		CPU:      machineProvisioning.CPU,
		MemoryMB: machineProvisioning.MemoryMB,
	}, nil
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
) (providers.ProvisionMachineResult, error) {
	options, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	bootEnvironment, err := providers.BuildManagedBootEnvironment(
		p.omnaraPublicURL,
		machineToken,
		options.StartupScript,
		machineEnv,
	)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if options.SleepAfterMS > 0 {
		bootEnvironment.DaemonEnv[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		bootEnvironment.DaemonEnv[daemonprotocol.WakeListenAddrEnvVar] = ":" +
			strconv.Itoa(daemonprotocol.WakeListenerPort)
		bootEnvironment.DaemonEnv[daemonprotocol.SleepPlatformEnvVar] = daemonprotocol.SleepPlatformBlaxel
	}
	startupEnvironmentPath := ""
	startupEnvironment := ""
	if options.StartupScript != "" {
		startupEnvironment, err = providers.RenderManagedStartupEnvironment(bootEnvironment.StartupEnv)
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
		startupEnvironmentPath, err = newStartupEnvironmentPath()
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
	}
	daemonEnv, err := bootEnvironment.ScopedDaemonEnv(startupEnvironmentPath)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	request := createSandboxRequest{
		Metadata: resourceMetadata{
			Name: name,
			Labels: map[string]string{
				"omnara-installation": installationID.String(),
				"omnara-machine":      machineID.String(),
			},
		},
		Spec: sandboxSpec{
			Region: options.Region,
			Runtime: sandboxRuntime{
				Image:  options.Image,
				Memory: *machineProvisioning.MemoryMB,
			},
		},
	}
	if options.SleepAfterMS > 0 {
		request.Spec.Runtime.Ports = []sandboxPort{{
			Name:     wakePortName,
			Protocol: wakePortProtocol,
			Target:   daemonprotocol.WakeListenerPort,
		}}
	}
	api := p.apiClient()
	target, err := api.CreateSandbox(ctx, request)
	if isConflict(err) {
		existing, found, getErr := api.GetSandbox(ctx, name)
		if getErr != nil {
			return providers.ProvisionMachineResult{}, getErr
		}
		if !found {
			return providers.ProvisionMachineResult{}, fmt.Errorf(
				"blaxel sandbox %q create conflicted but the sandbox is not usable",
				name,
			)
		}
		target = existing
	} else if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if !sandboxOwnedBy(target, name, installationID, machineID) {
		return providers.ProvisionMachineResult{}, fmt.Errorf(
			"blaxel sandbox %q does not have the expected ownership labels",
			name,
		)
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: name}
	if sandboxReplaceable(target.Status) {
		if err := api.DeleteSandbox(ctx, name); err != nil {
			return result, err
		}
		return providers.ProvisionMachineResult{}, fmt.Errorf(
			"blaxel sandbox %q was deleted; provisioning must be retried",
			name,
		)
	}
	if sandboxStatus(target.Status) != "DEPLOYED" {
		return result, fmt.Errorf("blaxel sandbox %q is not ready with status %q", name, target.Status)
	}
	daemonProcess, err := ensureDaemonProcess(
		ctx,
		api,
		target,
		daemonEnv,
		startupEnvironmentPath,
		startupEnvironment,
		options.SleepAfterMS > 0,
	)
	if err != nil {
		return result, err
	}
	if options.SleepAfterMS > 0 {
		supervisorPID, err := strconv.Atoi(daemonProcess.PID)
		if err != nil || supervisorPID <= 0 {
			return result, errors.New("blaxel daemon process is missing a valid pid")
		}
		awakeProcessName := daemonprotocol.BlaxelAwakeProcessName(supervisorPID)
		if err := waitForInitialAwakeProcess(ctx, api, target, awakeProcessName); err != nil {
			return result, err
		}
		result.SandboxURL = strings.TrimSpace(target.Metadata.URL)
	}
	return result, nil
}

func ensureDaemonProcess(
	ctx context.Context,
	api apiClient,
	target sandbox,
	daemonEnv map[string]string,
	startupEnvironmentPath string,
	startupEnvironment string,
	sleepEnabled bool,
) (sandboxProcess, error) {
	// Blaxel does not atomically reserve process names. The machine-pool
	// manager's provisioning claim therefore must serialize calls for one
	// machine; this function handles retries and ambiguous responses, not
	// concurrent successful starts outside that contract.
	expectedKeepAlive := !sleepEnabled
	providerSetup := ""
	if sleepEnabled {
		providerSetup = managedAwakeProcessSetupScript()
	}
	command := managedDaemonCommand(providerSetup, startupEnvironmentPath)
	existing, found, err := api.GetSandboxProcess(ctx, target, daemonProcessName)
	if err != nil {
		return sandboxProcess{}, err
	}
	if found && processStatus(existing.Status) == processStatusRunning {
		if err := validateAdoptableDaemonProcess(existing, expectedKeepAlive); err != nil {
			return sandboxProcess{}, err
		}
		return existing, nil
	}
	if startupEnvironmentPath != "" {
		// Do not sweep sibling attempt directories: another serialized attempt may
		// still own one after an ambiguous provider response. An abrupt backend or
		// sandbox crash can therefore leave a small root-only orphan for sandbox
		// lifetime; ordinary failure paths clean up their own attempt below.
		if err := api.CreateSandboxDirectory(
			ctx,
			target,
			path.Dir(startupEnvironmentPath),
		); err != nil {
			deleteStartupEnvironmentBestEffort(ctx, api, target, startupEnvironmentPath)
			return sandboxProcess{}, err
		}
		if err := api.UploadSandboxFile(
			ctx,
			target,
			startupEnvironmentPath,
			startupEnvironment,
		); err != nil {
			deleteStartupEnvironmentBestEffort(ctx, api, target, startupEnvironmentPath)
			return sandboxProcess{}, err
		}
	}
	process, startErr := api.StartSandboxProcess(ctx, target, processRequest{
		Name:              daemonProcessName,
		Command:           command,
		Env:               daemonEnv,
		KeepAlive:         expectedKeepAlive,
		Timeout:           0,
		WaitForCompletion: false,
	})
	if startErr != nil {
		existing, found, getErr := api.GetSandboxProcess(ctx, target, daemonProcessName)
		if getErr == nil && found && processStatus(existing.Status) == processStatusRunning {
			if err := validateAdoptableDaemonProcess(existing, expectedKeepAlive); err != nil {
				deleteStartupEnvironmentBestEffort(ctx, api, target, startupEnvironmentPath)
				return sandboxProcess{}, err
			}
			if existing.Command != command {
				deleteStartupEnvironmentBestEffort(ctx, api, target, startupEnvironmentPath)
			}
			return existing, nil
		}
		deleteStartupEnvironmentBestEffort(ctx, api, target, startupEnvironmentPath)
		return sandboxProcess{}, startErr
	}
	if processStatus(process.Status) != processStatusRunning {
		// sandbox-api sets running synchronously after cmd.Start succeeds; it has
		// no intermediate starting state.
		deleteStartupEnvironmentBestEffort(ctx, api, target, startupEnvironmentPath)
		return sandboxProcess{}, fmt.Errorf("blaxel daemon process started with status %q", process.Status)
	}
	if err := validateDaemonProcessKeepAlive(process, expectedKeepAlive); err != nil {
		deleteStartupEnvironmentBestEffort(ctx, api, target, startupEnvironmentPath)
		return sandboxProcess{}, err
	}
	return process, nil
}

func managedDaemonCommand(providerSetup string, startupEnvironmentPath string) string {
	command := providers.ManagedScopedBootScript(providerSetup)
	if startupEnvironmentPath == "" {
		return command
	}
	relativePath, ok := strings.CutPrefix(
		startupEnvironmentPath,
		startupEnvironmentDirectoryPrefix,
	)
	if !ok {
		return command
	}
	attemptID, ok := strings.CutSuffix(relativePath, "/"+startupEnvironmentFileName)
	if !ok || attemptID == "" || strings.Contains(attemptID, "/") {
		return command
	}
	// Keep the version header first so compatibility checks remain independent
	// of this per-attempt reconciliation marker.
	return strings.Replace(
		command,
		"\n",
		"\n"+bootstrapAttemptMarkerPrefix+attemptID+"\n",
		1,
	)
}

func newStartupEnvironmentPath() (string, error) {
	var suffix [startupEnvironmentRandomBytes]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate blaxel startup environment path: %w", err)
	}
	return startupEnvironmentDirectoryPrefix + hex.EncodeToString(suffix[:]) +
		"/" + startupEnvironmentFileName, nil
}

func deleteStartupEnvironmentBestEffort(
	ctx context.Context,
	api apiClient,
	target sandbox,
	startupEnvironmentPath string,
) {
	if startupEnvironmentPath == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		startupEnvironmentCleanupTimeout,
	)
	defer cancel()
	_ = api.DeleteSandboxPath(cleanupCtx, target, startupEnvironmentPath)
	// Use non-recursive deletion deliberately. If anything unexpected appeared
	// in this random directory, leave it in place rather than removing it.
	_ = api.DeleteSandboxPath(cleanupCtx, target, path.Dir(startupEnvironmentPath))
}

func validateAdoptableDaemonProcess(process sandboxProcess, expectedKeepAlive bool) error {
	if !providers.IsManagedScopedBootScript(process.Command) {
		return errors.New("existing Blaxel daemon process uses an incompatible bootstrap version")
	}
	return validateDaemonProcessKeepAlive(process, expectedKeepAlive)
}

func validateDaemonProcessKeepAlive(process sandboxProcess, expected bool) error {
	if process.KeepAlive != expected {
		return fmt.Errorf(
			"blaxel daemon process keep-alive is %t, want %t",
			process.KeepAlive,
			expected,
		)
	}
	return nil
}

func waitForInitialAwakeProcess(
	ctx context.Context,
	api apiClient,
	target sandbox,
	name string,
) error {
	return waitForInitialAwakeProcessWithDelay(ctx, api, target, name, waitForPollInterval)
}

func waitForInitialAwakeProcessWithDelay(
	ctx context.Context,
	api apiClient,
	target sandbox,
	name string,
	wait func(context.Context, time.Duration) error,
) error {
	delay := initialAwakeProcessPollInterval
	for {
		process, found, err := api.GetSandboxProcess(ctx, target, name)
		if err != nil {
			return err
		}
		if found {
			if processStatus(process.Status) == processStatusRunning && process.KeepAlive {
				return nil
			}
			return fmt.Errorf(
				"blaxel awake process %q is not running with keep-alive enabled",
				name,
			)
		}
		if err := wait(ctx, delay); err != nil {
			return fmt.Errorf("wait for initial blaxel awake process %q: %w", name, err)
		}
		delay = min(delay*2, maxAwakeProcessPollInterval)
	}
}

func waitForPollInterval(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *provider) WakeMachine(
	ctx context.Context,
	input providers.WakeMachineInput,
) error {
	return p.apiClient().WakeSandbox(ctx, sandbox{
		Metadata: resourceMetadata{
			Name: input.ProviderResourceID,
			URL:  input.SandboxURL,
		},
	})
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	lookup := providerResourceID
	if lookup == "" {
		lookup = expectedName
	}
	target, found, err := p.apiClient().GetSandbox(ctx, lookup)
	if err != nil || !found {
		return "", false, err
	}
	if !sandboxOwnedBy(target, expectedName, installationID, machineID) {
		return "", false, fmt.Errorf("blaxel sandbox %q does not have the expected ownership labels", lookup)
	}
	return expectedName, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	if providerResourceID == "" {
		return errors.New("provider resource id is required")
	}
	resourceID, found, err := p.InspectMachine(
		ctx,
		installationID,
		machineID,
		machineProvisioning,
		providerResourceID,
	)
	if err == nil && !found {
		resourceID, found, err = p.InspectMachine(ctx, installationID, machineID, machineProvisioning, "")
	}
	if err != nil || !found {
		return err
	}
	return p.apiClient().DeleteSandbox(ctx, resourceID)
}

func sandboxOwnedBy(
	target sandbox,
	name string,
	installationID, machineID storage.ID,
) bool {
	return target.Metadata.Name == name &&
		target.Metadata.Labels["omnara-installation"] == installationID.String() &&
		target.Metadata.Labels["omnara-machine"] == machineID.String()
}

func sandboxReplaceable(status string) bool {
	return slices.Contains([]string{"FAILED", "TERMINATED", "DEACTIVATED"}, sandboxStatus(status))
}

func sandboxStatus(status string) string {
	return strings.ToUpper(strings.TrimSpace(status))
}

func processStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}
