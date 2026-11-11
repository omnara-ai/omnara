package maritime

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout      = 2 * time.Minute
	pollDelay                = time.Second
	installationLabel        = "omnara-installation"
	machineLabel             = "omnara-machine"
	daemonRunTimeoutSeconds  = 30
	maxReportedOutputBytes   = 2048
	maxEnvKeys               = 128
	maxEnvValueBytes         = 32 * 1024
	maxEnvTotalBytes         = 256 * 1024
	reservedMachineTokenSize = 1024
)

//go:embed daemon_launcher.sh
var daemonLauncherScriptBody string

// envNamePattern matches the environment variable names Maritime accepts.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type provider struct {
	api          apiClient
	omnaraAPIURL string
	pollDelay    time.Duration
}

var _ providers.Provider = (*provider)(nil)

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
	return p.api.CheckConnection(ctx)
}

// ValidateMachineConfig rejects an environment that Maritime would refuse on
// the daemon launch, which carries the machine environment and startup script.
func (p *provider) ValidateMachineConfig(
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineEnv map[string]string,
) error {
	options, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return err
	}
	env, err := p.daemonEnv(options, strings.Repeat("x", reservedMachineTokenSize), machineEnv)
	if err != nil {
		return err
	}
	if len(env) > maxEnvKeys {
		return fmt.Errorf("maritime machine env must have at most %d variables, including Omnara's", maxEnvKeys)
	}
	total := 0
	for name, value := range env {
		if len(value) > maxEnvValueBytes {
			if name == providers.ManagedBootstrapScriptEnvVar || strings.HasPrefix(name, "OMNARA_") {
				return fmt.Errorf(
					"maritime machine config startup_script is too long; its encoded form must be at most %d bytes",
					maxEnvValueBytes,
				)
			}
			return fmt.Errorf("maritime machine env %s must be at most %d bytes", name, maxEnvValueBytes)
		}
		total += len(value)
	}
	if total > maxEnvTotalBytes {
		return fmt.Errorf(
			"maritime machine env and startup_script must total at most about %d bytes",
			maxEnvTotalBytes-reservedMachineTokenSize,
		)
	}
	return nil
}

// ProvisionMachine creates the sandbox under the machine's allocation name.
// Maritime returns the existing sandbox when a sandbox that has not ended
// already has that name, so every attempt converges on one sandbox and a
// retry never creates a duplicate.
func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
	_ bool,
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
	target, err := p.api.CreateSandbox(ctx, createSandboxRequest{
		Name:      name,
		From:      options.template(),
		VCPU:      *machineProvisioning.CPU,
		MemoryMiB: *machineProvisioning.MemoryMB,
		DiskGiB:   options.DiskGB,
		Metadata:  metadata,
		KeepAwake: true,
	})
	if err != nil {
		if rejectedBeforeCreate(err) {
			return providers.ProvisionMachineResult{}, fmt.Errorf(
				"create maritime sandbox: %w: %w",
				err,
				providers.ErrPermanent,
			)
		}
		return providers.ProvisionMachineResult{}, fmt.Errorf("create maritime sandbox: %w", err)
	}
	if err := validateOwnedSandbox(target, "", installationID, machineID); err != nil {
		return providers.ProvisionMachineResult{}, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if err := validateSandboxResources(target, machineProvisioning, options); err != nil {
		return result, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
	}
	target, err = p.awaitUsable(ctx, target)
	if err != nil {
		return result, err
	}
	if err := p.ensureDaemon(ctx, target, env); err != nil {
		return result, err
	}
	return result, nil
}

// awaitUsable waits until the sandbox has started. A paused sandbox, or one in
// error, is usable: the daemon launch resumes it.
func (p *provider) awaitUsable(ctx context.Context, target sandbox) (sandbox, error) {
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
					"wait for maritime sandbox %q to start: %w",
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
				return target, fmt.Errorf(
					"maritime sandbox %q disappeared while starting: %w",
					target.ID,
					providers.ErrResourceReplaced,
				)
			}
			target = refreshed
		}
		lastErr = nil
		switch target.State {
		case sandboxStateRunning, sandboxStatePaused, sandboxStateError:
			return target, nil
		case sandboxStateEnded:
			return target, fmt.Errorf(
				"maritime sandbox %q ended (%s) before Omnara started on it: %w",
				target.ID,
				target.EndedReason,
				providers.ErrResourceReplaced,
			)
		case sandboxStateStarting, sandboxStatePausing, sandboxStateResuming:
		}
	}
}

func validateSandboxResources(
	target sandbox,
	machineProvisioning executionstore.MachineProvisioningConfig,
	options providerOptions,
) error {
	resources := target.Resources
	switch {
	case resources.VCPU != 0 && resources.VCPU != *machineProvisioning.CPU:
		return fmt.Errorf(
			"maritime sandbox %q has %d vcpu, want %d",
			target.ID,
			resources.VCPU,
			*machineProvisioning.CPU,
		)
	case resources.MemoryMiB != 0 && resources.MemoryMiB != *machineProvisioning.MemoryMB:
		return fmt.Errorf(
			"maritime sandbox %q has %d MiB of memory, want %d",
			target.ID,
			resources.MemoryMiB,
			*machineProvisioning.MemoryMB,
		)
	case options.DiskGB != 0 && resources.DiskGiB != 0 && resources.DiskGiB != options.DiskGB:
		return fmt.Errorf(
			"maritime sandbox %q has a %d GiB disk, want %d",
			target.ID,
			resources.DiskGiB,
			options.DiskGB,
		)
	}
	return nil
}

func (p *provider) ensureDaemon(ctx context.Context, target sandbox, env map[string]string) error {
	request := daemonLauncherRequest(env)
	result, err := p.api.Run(ctx, target, request)
	if apiStatusCode(err) == http.StatusUnauthorized {
		// The sandbox secret was rotated; read the current one and try once more.
		refreshed, found, getErr := p.api.GetSandbox(ctx, target.ID)
		if getErr != nil || !found {
			return fmt.Errorf("start maritime daemon: %w", errors.Join(err, getErr))
		}
		result, err = p.api.Run(ctx, refreshed, request)
	}
	if err != nil {
		return fmt.Errorf("start maritime daemon: %w", err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		exitCode := "none"
		if result.ExitCode != nil {
			exitCode = fmt.Sprint(*result.ExitCode)
		}
		return fmt.Errorf(
			"maritime daemon launcher ended in state %q with exit code %s: %s",
			result.State,
			exitCode,
			tail(result.Stderr),
		)
	}
	return nil
}

func daemonLauncherRequest(env map[string]string) runRequest {
	return runRequest{
		Cmd: []string{
			"/bin/sh",
			"-c",
			daemonLauncherScript(),
			"omnara-launch",
			providers.ManagedDaemonLauncherArgs()[2],
		},
		Env:            env,
		TimeoutSeconds: daemonRunTimeoutSeconds,
	}
}

func daemonLauncherScript() string {
	return strings.TrimSpace(daemonLauncherScriptBody)
}

func tail(output string) string {
	output = strings.TrimSpace(output)
	if len(output) > maxReportedOutputBytes {
		output = output[len(output)-maxReportedOutputBytes:]
	}
	return output
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

func validateOwnedSandbox(current sandbox, resourceID string, installationID, machineID uuid.UUID) error {
	switch {
	case current.ID == "":
		return errors.New("maritime sandbox is missing its id")
	case resourceID != "" && current.ID != resourceID:
		return fmt.Errorf("maritime sandbox %q was returned for resource id %q", current.ID, resourceID)
	case !ownedBy(current, installationID, machineID):
		return fmt.Errorf("maritime sandbox %q does not have the expected ownership metadata", current.ID)
	}
	return nil
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
		if current.State == sandboxStateEnded || !ownedBy(current, installationID, machineID) {
			continue
		}
		if current.ID == "" {
			return sandbox{}, false, errors.New("owned maritime sandbox is missing its id")
		}
		if found.ID != "" {
			return sandbox{}, false, errors.New("multiple maritime sandboxes have the same Omnara ownership")
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
	if err := validateOwnedSandbox(current, providerResourceID, installationID, machineID); err != nil {
		return "", false, err
	}
	if current.State == sandboxStateEnded {
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

// rejectedBeforeCreate reports a create that Maritime refused outright, such
// as an invalid size, an unknown template, a missing plan, or a key without
// the sandboxes scope. Retrying it cannot succeed.
func rejectedBeforeCreate(err error) bool {
	status := apiStatusCode(err)
	return status >= 400 && status < 500 && !transientAPIError(err)
}
