package blaxel

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	env, err := providers.BuildManagedMachineEnv(
		p.omnaraPublicURL,
		machineToken,
		options.StartupScript,
		machineEnv,
	)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		env[daemonprotocol.WakeListenAddrEnvVar] = ":" +
			strconv.Itoa(daemonprotocol.WakeListenerPort)
		env[daemonprotocol.SleepPlatformEnvVar] = daemonprotocol.SleepPlatformBlaxel
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
				Envs:   sandboxEnvsFromMap(env),
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
		options.StartupScript,
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
	startupScript string,
	sleepEnabled bool,
) (sandboxProcess, error) {
	expectedKeepAlive := !sleepEnabled
	command := providers.ManagedBootScript()
	if sleepEnabled {
		command = managedBootScriptWithAwakeProcess(command)
	}
	existing, found, err := api.GetSandboxProcess(ctx, target, daemonProcessName)
	if err != nil {
		return sandboxProcess{}, err
	}
	if found && processStatus(existing.Status) == processStatusRunning {
		if err := validateDaemonProcessKeepAlive(existing, expectedKeepAlive); err != nil {
			return sandboxProcess{}, err
		}
		return existing, nil
	}
	process, startErr := api.StartSandboxProcess(ctx, target, processRequest{
		Name:              daemonProcessName,
		Command:           command,
		KeepAlive:         expectedKeepAlive,
		Timeout:           0,
		WaitForCompletion: false,
	})
	if startErr != nil {
		existing, found, getErr := api.GetSandboxProcess(ctx, target, daemonProcessName)
		if getErr == nil && found && processStatus(existing.Status) == processStatusRunning {
			if err := validateDaemonProcessKeepAlive(existing, expectedKeepAlive); err != nil {
				return sandboxProcess{}, err
			}
			return existing, nil
		}
		return sandboxProcess{}, startErr
	}
	if processStatus(process.Status) != processStatusRunning {
		return sandboxProcess{}, fmt.Errorf("blaxel daemon process started with status %q", process.Status)
	}
	if err := validateDaemonProcessKeepAlive(process, expectedKeepAlive); err != nil {
		return sandboxProcess{}, err
	}
	return process, nil
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
		timer := time.NewTimer(initialAwakeProcessPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for initial blaxel awake process %q: %w", name, ctx.Err())
		case <-timer.C:
		}
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

func sandboxEnvsFromMap(env map[string]string) []sandboxEnv {
	if len(env) == 0 {
		return nil
	}
	envs := make([]sandboxEnv, 0, len(env))
	for _, name := range slices.Sorted(maps.Keys(env)) {
		envs = append(envs, sandboxEnv{Name: name, Value: env[name]})
	}
	return envs
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
