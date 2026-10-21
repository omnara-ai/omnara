package superserve

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
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	provisioningTimeout      = 2 * time.Minute
	pollDelay                = time.Second
	installationLabel        = "omnara-installation"
	machineLabel             = "omnara-machine"
	daemonExecTimeoutSeconds = 30
	maxReportedOutputBytes   = 2048
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

func (p *provider) PrepareProvisioning(
	ctx context.Context,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	options, err := parseProviderOptions(machineProvisioning.ProviderOptions)
	if err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	if machineProvisioning.CPU != nil && machineProvisioning.MemoryMB != nil {
		return executionstore.MachineResourceFacts{
			CPU:      machineProvisioning.CPU,
			MemoryMB: machineProvisioning.MemoryMB,
		}, nil
	}
	candidate, err := p.lookupTemplate(ctx, options.template())
	if err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	return executionstore.MachineResourceFacts{CPU: &candidate.VCPU, MemoryMB: &candidate.MemoryMiB}, nil
}

func (p *provider) CheckConnection(ctx context.Context, _ executionstore.MachineProvisioningConfig) error {
	_, err := p.api.ListTemplates(ctx, "")
	return err
}

func (p *provider) lookupTemplate(ctx context.Context, name string) (template, error) {
	templates, err := p.api.ListTemplates(ctx, name)
	if err != nil {
		if transientAPIError(err) {
			return template{}, fmt.Errorf(
				"list superserve templates: %w: %w",
				storeerr.ErrMachineProviderUnavailable,
				err,
			)
		}
		return template{}, fmt.Errorf("list superserve templates: %w", err)
	}
	for _, candidate := range templates {
		if candidate.Name != name {
			continue
		}
		if candidate.Status != templateStatusReady {
			return template{}, fmt.Errorf(
				"superserve template %q is not ready (status %q)",
				name,
				candidate.Status,
			)
		}
		if candidate.VCPU <= 0 || candidate.MemoryMiB <= 0 {
			return template{}, fmt.Errorf("superserve template %q has invalid resources", name)
		}
		return candidate, nil
	}
	return template{}, fmt.Errorf("superserve template %q was not found", name)
}

func (*provider) ValidateMachineConfig(executionstore.MachineProvisioningConfig, map[string]string) error {
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
	options, err := parseProviderOptions(machineProvisioning.ProviderOptions)
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
		target, err = p.api.CreateSandbox(ctx, createSandboxRequest{
			Name:         name,
			FromTemplate: options.template(),
			Metadata:     metadata,
		})
		if err != nil {
			p.createAttempted = apiStatusCode(err) != http.StatusTooManyRequests && !isDialError(err)
			if p.createAttempted && rejectedBeforeCreate(err) {
				return providers.ProvisionMachineResult{}, fmt.Errorf(
					"create superserve sandbox: %w: %w",
					err,
					providers.ErrPermanent,
				)
			}
			return providers.ProvisionMachineResult{}, fmt.Errorf("create superserve sandbox: %w", err)
		}
	} else {
		var found bool
		target, found, err = p.findOwned(ctx, installationID, machineID)
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
		if !found {
			return providers.ProvisionMachineResult{}, errors.New(
				"superserve create outcome is unknown; waiting to discover the existing sandbox instead of creating a duplicate",
			)
		}
	}
	if target.ID == "" || !ownedBy(target, installationID, machineID) {
		return providers.ProvisionMachineResult{}, errors.New(
			"superserve sandbox does not have the expected ownership metadata",
		)
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	target, err = p.awaitActive(ctx, target)
	if err != nil {
		return result, err
	}
	if err := validateSandboxResources(target, machineProvisioning); err != nil {
		return result, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
	}
	if err := p.ensureDaemon(ctx, target, env); err != nil {
		return result, err
	}
	return result, nil
}

func (p *provider) awaitActive(ctx context.Context, target sandbox) (sandbox, error) {
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
					"wait for superserve sandbox %q to become active: %w",
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
				return target, fmt.Errorf("superserve sandbox %q disappeared while starting", target.ID)
			}
			target = refreshed
		}
		switch target.Status {
		case sandboxStatusActive:
			if target.AccessToken != "" {
				return target, nil
			}
		case sandboxStatusPaused:
		case sandboxStatusFailed, sandboxStatusDeleted:
			return target, fmt.Errorf("superserve sandbox %q is %s: %w", target.ID, target.Status, providers.ErrPermanent)
		default:
			lastErr = nil
			continue
		}
		activated, err := p.api.ActivateSandbox(ctx, target.ID)
		if err == nil {
			target = activated
			if target.Status == sandboxStatusActive && target.AccessToken != "" {
				return target, nil
			}
		} else if !transientAPIError(err) {
			return target, err
		}
		lastErr = err
	}
}

func validateSandboxResources(target sandbox, machineProvisioning executionstore.MachineProvisioningConfig) error {
	if machineProvisioning.CPU != nil && target.VCPUCount != 0 && target.VCPUCount != *machineProvisioning.CPU {
		return fmt.Errorf(
			"superserve sandbox %q has %d vcpu, want %d",
			target.ID,
			target.VCPUCount,
			*machineProvisioning.CPU,
		)
	}
	if machineProvisioning.MemoryMB != nil && target.MemoryMiB != 0 && target.MemoryMiB != *machineProvisioning.MemoryMB {
		return fmt.Errorf(
			"superserve sandbox %q has %d MiB of memory, want %d",
			target.ID,
			target.MemoryMiB,
			*machineProvisioning.MemoryMB,
		)
	}
	return nil
}

func (p *provider) ensureDaemon(ctx context.Context, target sandbox, env map[string]string) error {
	session := execSession{
		SandboxID:   target.ID,
		AccessToken: target.AccessToken,
		RoutingHint: target.RoutingHint,
	}
	request := daemonLauncherRequest(env)
	result, err := p.api.Exec(ctx, session, request)
	if err != nil && sandboxUnavailable(err) {
		activated, activateErr := p.api.ActivateSandbox(ctx, target.ID)
		if activateErr != nil {
			return fmt.Errorf("start superserve daemon: %w", errors.Join(err, activateErr))
		}
		session.AccessToken = activated.AccessToken
		session.RoutingHint = activated.RoutingHint
		result, err = p.api.Exec(ctx, session, request)
	}
	if err != nil {
		return fmt.Errorf("start superserve daemon: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"superserve daemon launcher exited with status %d: %s",
			result.ExitCode,
			tail(result.Stderr),
		)
	}
	return nil
}

func daemonLauncherRequest(env map[string]string) execRequest {
	return execRequest{
		Command:        "/bin/sh",
		Args:           []string{"-c", daemonLauncherScript(), "omnara-launch", providers.ManagedDaemonLauncherArgs()[2]},
		Env:            env,
		TimeoutSeconds: daemonExecTimeoutSeconds,
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
		if current.Status == sandboxStatusDeleted || !ownedBy(current, installationID, machineID) {
			continue
		}
		if current.ID == "" {
			return sandbox{}, false, errors.New("owned superserve sandbox is missing its id")
		}
		if found.ID != "" {
			return sandbox{}, false, errors.New("multiple superserve sandboxes have the same Omnara ownership")
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
		return "", false, errors.New("superserve sandbox is missing its id")
	case providerResourceID != "" && current.ID != providerResourceID:
		return "", false, fmt.Errorf(
			"superserve sandbox %q was returned for resource id %q",
			current.ID,
			providerResourceID,
		)
	case !ownedBy(current, installationID, machineID):
		return "", false, fmt.Errorf(
			"superserve sandbox %q does not have the expected ownership metadata",
			current.ID,
		)
	case current.Status == sandboxStatusDeleted:
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

func rejectedBeforeCreate(err error) bool {
	status := apiStatusCode(err)
	return status >= 400 && status < 500 && !transientAPIError(err)
}
