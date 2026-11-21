package novita

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	provisioningTimeout  = time.Minute
	createTimeoutSeconds = 300
	maxTimeoutSeconds    = 30 * 24 * 60 * 60
	daemonProcessTag     = "omnara-daemon"
	launcherLockPath     = "/tmp/omnara-launcher.lock"
	installationLabel    = "omnara-installation"
	machineLabel         = "omnara-machine"
	// longRunningLabel keeps the sandbox alive beyond the platform's default
	// lifetime cap so the machine lasts as long as the agent holds it.
	longRunningLabel = "long_running"
)

var errTemplateNotFound = errors.New("not found among your team's templates or Novita's public templates")

type provider struct {
	api             apiClient
	omnaraAPIURL    string
	createAttempted bool
}

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
	resolved, err := p.findTemplate(ctx, options.Template)
	if err != nil {
		return executionstore.MachineResourceFacts{}, fmt.Errorf("find novita template %q: %w", options.Template, err)
	}
	cpu, memoryMB := resolved.size()
	if cpu <= 0 || memoryMB <= 0 {
		return executionstore.MachineResourceFacts{}, fmt.Errorf("novita template %q has no ready build", options.Template)
	}
	return executionstore.MachineResourceFacts{CPU: &cpu, MemoryMB: &memoryMB}, nil
}

func (p *provider) findTemplate(ctx context.Context, ref string) (template, error) {
	templateID, err := p.resolveTemplateID(ctx, ref)
	if err != nil {
		return template{}, err
	}
	resolved, err := p.api.GetTemplate(ctx, templateID)
	switch {
	case err == nil:
		if resolved.TemplateID == "" {
			resolved.TemplateID = templateID
		}
		return resolved, nil
	case isNotFound(err):
		return template{}, errTemplateNotFound
	case transientAPIError(err):
		return template{}, fmt.Errorf("%w: %w", storeerr.ErrMachineProviderUnavailable, err)
	default:
		return template{}, err
	}
}

// resolveTemplateID turns a template name into its ID. A reference the alias
// lookup does not know is used as an ID directly, which covers templates that
// are referenced by ID and public templates outside the team's namespace.
func (p *provider) resolveTemplateID(ctx context.Context, ref string) (string, error) {
	templateID, err := p.api.ResolveTemplateAlias(ctx, ref)
	switch {
	case err == nil && templateID != "":
		return templateID, nil
	case err == nil, isNotFound(err), apiStatusCode(err) == http.StatusForbidden:
		return ref, nil
	case transientAPIError(err):
		return "", fmt.Errorf("%w: %w", storeerr.ErrMachineProviderUnavailable, err)
	default:
		return "", err
	}
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
	var result providers.ProvisionMachineResult
	options, err := parseProviderOptions(machineProvisioning.ProviderOptions)
	if err != nil {
		return result, err
	}
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		return result, err
	}
	env, err := providers.BuildManagedMachineEnv(p.omnaraAPIURL, machineToken, options.StartupScript, machineEnv)
	if err != nil {
		return result, err
	}
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	if firstAttempt && !p.createAttempted {
		p.createAttempted = true
		created, err := p.api.CreateSandbox(ctx, createSandboxRequest{
			TemplateID: options.Template,
			Timeout:    createTimeoutSeconds,
			Secure:     true,
			Metadata:   creationMetadata(metadata),
			Network:    networkConfig{AllowPublicTraffic: false},
		})
		if err != nil {
			status := apiStatusCode(err)
			if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || isDialError(err) {
				p.createAttempted = false
			} else if status >= 400 && status < 500 && status != http.StatusRequestTimeout {
				return result, fmt.Errorf("create novita sandbox: %w: %w", err, providers.ErrPermanent)
			}
			return result, fmt.Errorf("create novita sandbox: %w", err)
		}
		result.ProviderResourceID = created.SandboxID
	} else {
		id, found, err := p.InspectMachine(ctx, installationID, machineID, machineProvisioning, "")
		if err != nil {
			return result, err
		}
		if !found {
			return result, errors.New(
				"novita create outcome is unknown; waiting to discover the existing sandbox instead of creating a duplicate",
			)
		}
		result.ProviderResourceID = id
	}
	if result.ProviderResourceID == "" {
		return providers.ProvisionMachineResult{}, errors.New("novita sandbox is missing its id")
	}
	target, found, err := p.api.GetSandbox(ctx, result.ProviderResourceID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, fmt.Errorf("novita sandbox %q was not found", result.ProviderResourceID)
	}
	if !ownedBy(target, metadata) {
		return result, fmt.Errorf("novita sandbox %q does not have the expected ownership metadata", target.SandboxID)
	}
	if machineProvisioning.CPU == nil || machineProvisioning.MemoryMB == nil ||
		target.CPUCount != *machineProvisioning.CPU || target.MemoryMB != *machineProvisioning.MemoryMB {
		return result, fmt.Errorf(
			"novita sandbox %q has cpu=%d memory_mb=%d, not the resolved machine size: %w",
			target.SandboxID,
			target.CPUCount,
			target.MemoryMB,
			providers.ErrPermanent,
		)
	}
	if err := p.api.ExtendTimeout(ctx, target.SandboxID, maxTimeoutSeconds); err != nil {
		return result, err
	}
	processes, err := p.api.ListProcesses(ctx, target)
	if err != nil {
		return result, err
	}
	if slices.ContainsFunc(processes, func(current process) bool { return current.Tag == daemonProcessTag }) {
		return result, nil
	}
	launcher := daemonLauncherArgs()
	return result, p.api.StartProcess(ctx, target, startProcessRequest{
		Process: processConfig{Cmd: launcher[0], Args: launcher[1:], Envs: env},
		Tag:     daemonProcessTag,
		Stdin:   false,
	})
}

func daemonLauncherArgs() []string {
	args := providers.ManagedDaemonLauncherArgs()
	args[2] = launcherGuard(launcherLockPath) + args[2]
	return args
}

func launcherGuard(lockPath string) string {
	return "l=" + lockPath + `;n=0;while ! ln -s $$ "$l.$n" 2>/dev/null;do p=$(readlink "$l.$n" 2>/dev/null)||exit 1;` +
		`kill -0 "$p" 2>/dev/null&&exit 0;n=$((n+1));done;`
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	var target sandbox
	if providerResourceID == "" {
		owned, err := p.api.FindSandboxes(ctx, metadata)
		if err != nil || len(owned) == 0 {
			return "", false, err
		}
		if len(owned) > 1 {
			return "", false, errors.New("multiple novita sandboxes have the same Omnara ownership")
		}
		target = owned[0]
	} else {
		var found bool
		target, found, err = p.api.GetSandbox(ctx, providerResourceID)
		if err != nil || !found {
			return "", false, err
		}
	}
	if target.SandboxID == "" || !ownedBy(target, metadata) {
		return "", false, fmt.Errorf("novita sandbox %q does not have the expected ownership metadata", target.SandboxID)
	}
	return target.SandboxID, true, nil
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
	resourceID, found, err := p.InspectMachine(ctx, installationID, machineID, machineProvisioning, providerResourceID)
	if err != nil || !found {
		return err
	}
	return p.api.DeleteSandbox(ctx, resourceID)
}

func (p *provider) CheckConnection(ctx context.Context, _ executionstore.MachineProvisioningConfig) error {
	_, err := p.api.FindSandboxes(ctx, nil)
	return err
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

// creationMetadata adds the lifecycle labels to the ownership labels. Only the
// ownership labels take part in ownership checks and lookups.
func creationMetadata(ownership map[string]string) map[string]string {
	metadata := make(map[string]string, len(ownership)+1)
	for key, value := range ownership {
		metadata[key] = value
	}
	metadata[longRunningLabel] = "true"
	return metadata
}

func ownedBy(target sandbox, metadata map[string]string) bool {
	return target.Metadata[installationLabel] == metadata[installationLabel] &&
		target.Metadata[machineLabel] == metadata[machineLabel]
}
