package agent37

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	// A create returns once the instance is running, which includes pulling
	// the template image onto a host that has not run it before.
	provisioningTimeout    = 5 * time.Minute
	pollDelay              = time.Second
	installationLabel      = "omnara-installation"
	machineLabel           = "omnara-machine"
	maxReportedOutputBytes = 2048
	// Agent37 caps an exec request body at 128 KiB.
	maxLaunchCommandBytes = 120 * 1024
)

//go:embed daemon_launcher.sh
var daemonLauncherScriptBody string

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type provider struct {
	api             apiClient
	omnaraAPIURL    string
	pollDelay       time.Duration
	createAttempted bool
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
	return p.api.ListTemplates(ctx)
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
	if size := len(daemonLaunchCommand(env)); size > maxLaunchCommandBytes {
		return fmt.Errorf(
			"agent37 machine environment and startup script take %d bytes, more than the %d an agent37 machine accepts",
			size,
			maxLaunchCommandBytes,
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
	var target instance
	if firstAttempt && !p.createAttempted {
		p.createAttempted = true
		target, err = p.api.CreateInstance(ctx, createInstanceRequest{
			Template: options.Template,
			Name:     name,
			Metadata: metadata,
			Resources: instanceResources{
				CPU:      *machineProvisioning.CPU,
				MemoryGB: *machineProvisioning.MemoryMB / 1024,
				DiskGB:   options.DiskGB,
			},
		})
		if err != nil {
			status := apiStatusCode(err)
			if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || isDialError(err) {
				p.createAttempted = false
			} else if status >= 400 && status < 500 && status != http.StatusRequestTimeout {
				return providers.ProvisionMachineResult{}, fmt.Errorf(
					"create agent37 instance: %w: %w",
					err,
					providers.ErrPermanent,
				)
			}
			return providers.ProvisionMachineResult{}, fmt.Errorf("create agent37 instance: %w", err)
		}
	} else {
		var found bool
		target, found, err = p.findOwned(ctx, installationID, machineID)
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
		if !found {
			return providers.ProvisionMachineResult{}, errors.New(
				"agent37 create outcome is unknown; waiting to discover the existing instance instead of creating a duplicate",
			)
		}
	}
	if target.ID == "" || !ownedBy(target, installationID, machineID) {
		return providers.ProvisionMachineResult{}, errors.New(
			"agent37 instance does not have the expected ownership metadata",
		)
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	target, err = p.awaitRunning(ctx, target)
	if err != nil {
		return result, err
	}
	if err := validateInstanceResources(target, machineProvisioning); err != nil {
		return result, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
	}
	if err := p.ensureDaemon(ctx, target.ID, env); err != nil {
		return result, err
	}
	return result, nil
}

func (p *provider) awaitRunning(ctx context.Context, target instance) (instance, error) {
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
					"wait for agent37 instance %q to run: %w",
					target.ID,
					errors.Join(lastErr, ctx.Err()),
				)
			case <-time.After(delay):
			}
			refreshed, found, err := p.api.GetInstance(ctx, target.ID)
			if err != nil {
				if !transientAPIError(err) {
					return target, err
				}
				lastErr = err
				continue
			}
			if !found {
				return target, fmt.Errorf("agent37 instance %q disappeared while starting", target.ID)
			}
			target = refreshed
		}
		lastErr = nil
		switch target.Status {
		case instanceStatusRunning:
			return target, nil
		case instanceStatusStopped, instanceStatusSleeping:
			if err := p.api.StartInstance(ctx, target.ID); err != nil {
				if !transientAPIError(err) {
					return target, err
				}
				lastErr = err
			}
		case instanceStatusFailed, instanceStatusDeleting, instanceStatusDeleted:
			return target, fmt.Errorf("agent37 instance %q is %s: %w", target.ID, target.Status, providers.ErrPermanent)
		case instanceStatusProvisioning, instanceStatusStarting, instanceStatusStopping, instanceStatusRestarting,
			instanceStatusUpdating, instanceStatusWaking:
		}
	}
}

func validateInstanceResources(target instance, machineProvisioning executionstore.MachineProvisioningConfig) error {
	if machineProvisioning.CPU != nil && target.Resources.CPU != *machineProvisioning.CPU {
		return fmt.Errorf(
			"agent37 instance %q has %d vcpu, want %d",
			target.ID,
			target.Resources.CPU,
			*machineProvisioning.CPU,
		)
	}
	if machineProvisioning.MemoryMB != nil && target.Resources.MemoryGB*1024 != *machineProvisioning.MemoryMB {
		return fmt.Errorf(
			"agent37 instance %q has %d GB of memory, want %d MB",
			target.ID,
			target.Resources.MemoryGB,
			*machineProvisioning.MemoryMB,
		)
	}
	return nil
}

func (p *provider) ensureDaemon(ctx context.Context, id string, env map[string]string) error {
	result, err := p.api.Exec(ctx, id, daemonLaunchCommand(env))
	if err != nil {
		return fmt.Errorf("start agent37 daemon: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"agent37 daemon launcher exited with status %d: %s",
			result.ExitCode,
			tail(result.Stderr),
		)
	}
	return nil
}

// daemonLaunchCommand exports the machine environment and runs the launcher,
// which starts the managed daemon detached so it outlives the exec call. The
// environment travels with the command rather than being stored on the
// instance.
func daemonLaunchCommand(env map[string]string) string {
	var command strings.Builder
	for _, name := range slices.Sorted(maps.Keys(env)) {
		command.WriteString("export " + name + "=" + shellQuote(env[name]) + "\n")
	}
	command.WriteString(
		"exec /bin/sh -c " + shellQuote(daemonLauncherScript()) +
			" omnara-launch " + shellQuote(providers.ManagedDaemonLauncherArgs()[2]),
	)
	return command.String()
}

func daemonLauncherScript() string {
	return strings.TrimSpace(daemonLauncherScriptBody)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
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

func ownedBy(current instance, installationID, machineID uuid.UUID) bool {
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		return false
	}
	for key, want := range metadata {
		if value, ok := current.Metadata[key].(string); !ok || value != want {
			return false
		}
	}
	return true
}

// findOwned scans the workspace's instances because the Agent37 list endpoint
// has no metadata filter.
func (p *provider) findOwned(
	ctx context.Context,
	installationID, machineID uuid.UUID,
) (instance, bool, error) {
	instances, err := p.api.ListInstances(ctx)
	if err != nil {
		return instance{}, false, err
	}
	var found instance
	for _, current := range instances {
		if current.Status == instanceStatusDeleted || !ownedBy(current, installationID, machineID) {
			continue
		}
		if current.ID == "" {
			return instance{}, false, errors.New("owned agent37 instance is missing its id")
		}
		if found.ID != "" {
			return instance{}, false, errors.New("multiple agent37 instances have the same Omnara ownership")
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
	var current instance
	var found bool
	var err error
	if providerResourceID == "" {
		current, found, err = p.findOwned(ctx, installationID, machineID)
	} else {
		current, found, err = p.api.GetInstance(ctx, providerResourceID)
	}
	if err != nil || !found {
		return "", false, err
	}
	switch {
	case current.ID == "":
		return "", false, errors.New("agent37 instance is missing its id")
	case providerResourceID != "" && current.ID != providerResourceID:
		return "", false, fmt.Errorf(
			"agent37 instance %q was returned for resource id %q",
			current.ID,
			providerResourceID,
		)
	case !ownedBy(current, installationID, machineID):
		return "", false, fmt.Errorf(
			"agent37 instance %q does not have the expected ownership metadata",
			current.ID,
		)
	case current.Status == instanceStatusDeleted:
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
	return p.api.DeleteInstance(ctx, resourceID)
}
