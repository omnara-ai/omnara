package groundbox

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout     = 2 * time.Minute
	lifecycleRequestTimeout = 45 * time.Second
	wakeTimeout             = 30 * time.Second
	pollDelay               = time.Second
	sandboxTimeoutMinutes   = 30 * 24 * 60
	keeperTimeoutSeconds    = 30 * 24 * 60 * 60
	maxCommandRequestBytes  = 1000 * 1000
	installationLabel       = "omnara-installation"
	machineLabel            = "omnara-machine"
	daemonPIDPath           = "/tmp/omnara-daemon.pid"
	daemonLogPath           = "/tmp/omnarad.log"
)

//go:embed keep_awake.sh
var keepAwakeScript string

var (
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	bootCommand    = keeperCommand("boot")
	wakeCommand    = keeperCommand("wake")
)

type provider struct {
	api             apiClient
	omnaraAPIURL    string
	pollDelay       time.Duration
	createAttempted bool
}

var (
	_ providers.Provider        = (*provider)(nil)
	_ providers.RuntimeProvider = (*provider)(nil)
	_ providers.MachineWaker    = (*provider)(nil)
)

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

func (p *provider) CheckConnection(ctx context.Context, _ executionstore.MachineProvisioningConfig) error {
	_, err := p.api.ListSandboxes(ctx, nil)
	return err
}

func (p *provider) ValidateMachineConfig(
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineEnv map[string]string,
) error {
	options, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return err
	}
	env, err := p.daemonEnv(options, "machine-token", machineEnv)
	if err != nil {
		return err
	}
	request, err := json.Marshal(keeperRequest(bootCommand, env))
	if err != nil {
		return err
	}
	if size := len(request); size > maxCommandRequestBytes {
		return fmt.Errorf(
			"groundbox machine environment and startup script take %d bytes, more than the %d a command accepts",
			size,
			maxCommandRequestBytes,
		)
	}
	return nil
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
	firstAttempt bool,
) (providers.ProvisionMachineResult, error) {
	options, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	env, err := p.daemonEnv(options, machineToken, machineEnv)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	var target sandbox
	if firstAttempt && !p.createAttempted {
		p.createAttempted = true
		target, err = p.api.CreateSandbox(ctx, name, createSandboxRequest{
			CPUs:               *machineProvisioning.CPU,
			MemoryGB:           *machineProvisioning.MemoryMB / mebibytesPerGB,
			Template:           options.Template,
			TimeoutMinutes:     sandboxTimeoutMinutes,
			IdleTimeoutSeconds: options.idleTimeoutSeconds(),
			Metadata:           metadata,
		})
		if err != nil {
			status := apiStatusCode(err)
			if status == http.StatusTooManyRequests || isDialError(err) {
				p.createAttempted = false
			} else if status >= 400 && status < 500 && !transientAPIError(err) {
				return providers.ProvisionMachineResult{}, fmt.Errorf(
					"create groundbox sandbox: %w: %w",
					err,
					providers.ErrPermanent,
				)
			}
			return providers.ProvisionMachineResult{}, fmt.Errorf("create groundbox sandbox: %w", err)
		}
	} else {
		var found bool
		target, found, err = p.findOwned(ctx, installationID, machineID)
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
		if !found {
			return providers.ProvisionMachineResult{}, errors.New(
				"groundbox create outcome is unknown; waiting to discover the existing sandbox instead of creating a duplicate",
			)
		}
	}
	if target.ID == "" || !ownedBy(target, installationID, machineID) {
		return providers.ProvisionMachineResult{}, errors.New(
			"groundbox sandbox does not have the expected ownership metadata",
		)
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	target, err = p.awaitRunning(ctx, target)
	if err != nil {
		return result, err
	}
	if err := validateSandboxResources(target, machineProvisioning); err != nil {
		return result, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
	}
	if err := p.ensureDaemon(ctx, target.ID, env); err != nil {
		return result, err
	}
	if options.SleepAfterMS > 0 {
		result.SandboxURL = defaultAPIBaseURL + sandboxPath(target.ID)
	}
	return result, nil
}

func (p *provider) awaitRunning(ctx context.Context, target sandbox) (sandbox, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			delay := p.pollDelay
			if retryAfter, ok := providers.RetryAfter(lastErr); ok {
				delay = max(delay, retryAfter)
			}
			select {
			case <-ctx.Done():
				return target, fmt.Errorf(
					"wait for groundbox sandbox %q to run: %w",
					target.ID,
					errors.Join(lastErr, ctx.Err()),
				)
			case <-time.After(delay):
			}
			refreshed, found, err := p.api.GetSandbox(ctx, target.ID)
			if err != nil {
				if !transientAPIError(err) {
					return target, err
				}
				lastErr = err
				continue
			}
			if !found {
				return target, fmt.Errorf("groundbox sandbox %q disappeared while starting", target.ID)
			}
			target = refreshed
		}
		lastErr = nil
		switch target.Status {
		case sandboxStatusRunning:
			return target, nil
		case sandboxStatusPaused, sandboxStatusShutDown:
			started, err := p.restart(ctx, target)
			if err == nil && started.ID == target.ID && started.Status == sandboxStatusRunning {
				return started, nil
			}
			lastErr = err
		case sandboxStatusStopped, sandboxStatusFailed:
			return target, fmt.Errorf("%w: %w", sandboxEndedError(target), providers.ErrPermanent)
		case sandboxStatusQueued, sandboxStatusStarting, sandboxStatusPausing, sandboxStatusResuming,
			sandboxStatusShuttingDown, sandboxStatusStopping:
		}
		if lastErr != nil && !transientAPIError(lastErr) {
			return target, lastErr
		}
	}
}

func (p *provider) restart(ctx context.Context, target sandbox) (sandbox, error) {
	if target.Status == sandboxStatusShutDown {
		return p.api.StartSandbox(ctx, target.ID)
	}
	return p.api.ResumeSandbox(ctx, target.ID)
}

func sandboxEndedError(target sandbox) error {
	if target.Error == "" {
		return fmt.Errorf("groundbox sandbox %q is %s", target.ID, target.Status)
	}
	return fmt.Errorf("groundbox sandbox %q is %s: %s", target.ID, target.Status, target.Error)
}

func validateSandboxResources(target sandbox, machineProvisioning executionstore.MachineProvisioningConfig) error {
	if target.CPUs != *machineProvisioning.CPU {
		return fmt.Errorf(
			"groundbox sandbox %q has %d vcpu, want %d",
			target.ID,
			target.CPUs,
			*machineProvisioning.CPU,
		)
	}
	if target.MemoryGB*mebibytesPerGB != *machineProvisioning.MemoryMB {
		return fmt.Errorf(
			"groundbox sandbox %q has %d GB of memory, want %d MB",
			target.ID,
			target.MemoryGB,
			*machineProvisioning.MemoryMB,
		)
	}
	return nil
}

func (p *provider) ensureDaemon(ctx context.Context, id string, env map[string]string) error {
	running, err := p.api.ListRunningCommands(ctx, id)
	if err != nil {
		return fmt.Errorf("list groundbox commands: %w", err)
	}
	if slices.ContainsFunc(running, func(current command) bool {
		return current.Cmd == bootCommand || current.Cmd == wakeCommand
	}) {
		return nil
	}
	if _, err := p.api.StartCommand(ctx, id, keeperRequest(bootCommand, env)); err != nil {
		return fmt.Errorf("start groundbox daemon: %w", err)
	}
	return nil
}

func (p *provider) WakeMachine(ctx context.Context, input providers.WakeMachineInput) error {
	if input.ProviderResourceID == "" {
		return errors.New("groundbox wake requires a provider resource id")
	}
	ctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	current, found, err := p.api.GetSandbox(ctx, input.ProviderResourceID)
	if err != nil {
		return fmt.Errorf("get groundbox sandbox %q to wake: %w", input.ProviderResourceID, err)
	}
	if !found {
		return fmt.Errorf("groundbox sandbox %q was not found", input.ProviderResourceID)
	}
	if _, err := p.awaitRunning(ctx, current); err != nil {
		return fmt.Errorf("wake groundbox sandbox %q: %w", input.ProviderResourceID, err)
	}
	if _, err := p.api.StartCommand(ctx, input.ProviderResourceID, keeperRequest(wakeCommand, nil)); err != nil {
		return fmt.Errorf("wake groundbox daemon on sandbox %q: %w", input.ProviderResourceID, err)
	}
	return nil
}

func keeperRequest(cmd string, env map[string]string) startCommandRequest {
	return startCommandRequest{Cmd: cmd, Env: env, TimeoutS: keeperTimeoutSeconds}
}

func keeperCommand(mode string) string {
	return "omnara_mode=" + mode + "\n" +
		"omnara_awake_file=" + daemonprotocol.GroundboxAwakeControlFilePath + "\n" +
		"omnara_daemon_pid_file=" + daemonPIDPath + "\n" +
		"omnara_daemon_log=" + daemonLogPath + "\n" +
		"omnara_wake_port=" + strconv.Itoa(daemonprotocol.WakeListenerPort) + "\n" +
		"omnara_daemon_launcher=" + shellQuote(providers.ManagedDaemonLauncherArgs()[2]) + "\n" +
		strings.TrimSpace(keepAwakeScript)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (p *provider) daemonEnv(
	options providerOptions,
	machineToken string,
	machineEnv map[string]string,
) (map[string]string, error) {
	env, err := providers.BuildManagedMachineEnv(p.omnaraAPIURL, machineToken, options.StartupScript, machineEnv)
	if err != nil {
		return nil, err
	}
	maps.DeleteFunc(env, func(name, _ string) bool { return !envNamePattern.MatchString(name) })
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		env[daemonprotocol.WakeListenAddrEnvVar] = "127.0.0.1:" + strconv.Itoa(daemonprotocol.WakeListenerPort)
		env[daemonprotocol.SleepPlatformEnvVar] = daemonprotocol.SleepPlatformGroundbox
	}
	return env, nil
}

func ownershipMetadata(installationID, machineID uuid.UUID) (map[string]string, error) {
	if installationID == uuid.Nil || machineID == uuid.Nil {
		return nil, errors.New("installation and machine ids are required")
	}
	installationOwner, err := publicid.Encode(publicid.KindInstallation, installationID)
	if err != nil {
		return nil, err
	}
	machineOwner, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		return nil, err
	}
	return map[string]string{installationLabel: installationOwner, machineLabel: machineOwner}, nil
}

func ownedBy(current sandbox, installationID, machineID uuid.UUID) bool {
	metadata, err := ownershipMetadata(installationID, machineID)
	return err == nil &&
		current.Metadata[installationLabel] == metadata[installationLabel] &&
		current.Metadata[machineLabel] == metadata[machineLabel]
}

func sandboxEnded(status sandboxStatus) bool {
	return status == sandboxStatusStopped || status == sandboxStatusFailed
}

func (p *provider) findOwned(
	ctx context.Context,
	installationID, machineID uuid.UUID,
) (sandbox, bool, error) {
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		return sandbox{}, false, err
	}
	sandboxes, err := p.api.ListSandboxes(ctx, metadata)
	if err != nil {
		return sandbox{}, false, err
	}
	var found sandbox
	for _, current := range sandboxes {
		if sandboxEnded(current.Status) || !ownedBy(current, installationID, machineID) {
			continue
		}
		if current.ID == "" {
			return sandbox{}, false, errors.New("owned groundbox sandbox is missing its id")
		}
		if found.ID != "" {
			return sandbox{}, false, errors.New("multiple groundbox sandboxes have the same Omnara ownership")
		}
		found = current
	}
	return found, found.ID != "", nil
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	var current sandbox
	var found bool
	var err error
	if providerResourceID == "" {
		current, found, err = p.findOwned(ctx, installationID, machineID)
	} else {
		current, found, err = p.api.GetSandbox(ctx, providerResourceID)
	}
	if err != nil || !found {
		return "", false, err
	}
	switch {
	case current.ID == "":
		return "", false, errors.New("groundbox sandbox is missing its id")
	case providerResourceID != "" && current.ID != providerResourceID:
		return "", false, fmt.Errorf(
			"groundbox sandbox %q was returned for resource id %q",
			current.ID,
			providerResourceID,
		)
	case !ownedBy(current, installationID, machineID):
		return "", false, fmt.Errorf(
			"groundbox sandbox %q does not have the expected ownership metadata",
			current.ID,
		)
	case sandboxEnded(current.Status):
		return "", false, nil
	}
	return current.ID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
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
	return p.api.DeleteSandbox(ctx, resourceID)
}
