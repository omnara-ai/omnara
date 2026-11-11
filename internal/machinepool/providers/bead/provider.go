package bead

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
)

const (
	provisioningTimeout = 2 * time.Minute
	daemonProcessTag    = "omnara-daemon"
	launcherLockPath    = "/tmp/omnara-launcher.lock"
	installationLabel   = "omnara-installation"
	machineLabel        = "omnara-machine"
)

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
		return executionstore.MachineResourceFacts{CPU: machineProvisioning.CPU, MemoryMB: machineProvisioning.MemoryMB}, nil
	}
	templates, err := p.api.ListTemplates(ctx)
	if err != nil {
		return executionstore.MachineResourceFacts{}, fmt.Errorf("list bead templates: %w", err)
	}
	for _, current := range templates {
		if current.TemplateID == options.template() || slices.Contains(current.Names, options.template()) {
			if current.CPUCount <= 0 || current.MemoryMB <= 0 {
				return executionstore.MachineResourceFacts{}, errors.New("bead template has no ready size")
			}
			shapes, err := p.api.ListShapes(ctx)
			if err != nil {
				return executionstore.MachineResourceFacts{}, fmt.Errorf("list bead shapes: %w", err)
			}
			var selected *machineShape
			for _, shape := range shapes {
				if shape.CPU < current.CPUCount || shape.MemoryMB < current.MemoryMB {
					continue
				}
				if selected == nil || shape.CPU < selected.CPU ||
					(shape.CPU == selected.CPU && shape.MemoryMB < selected.MemoryMB) {
					selected = &shape
				}
			}
			if selected == nil {
				return executionstore.MachineResourceFacts{}, errors.New("no bead shape fits the approved template")
			}
			return executionstore.MachineResourceFacts{CPU: &selected.CPU, MemoryMB: &selected.MemoryMB}, nil
		}
	}
	return executionstore.MachineResourceFacts{}, fmt.Errorf("bead template %q not found", options.template())
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
	if machineProvisioning.CPU == nil || machineProvisioning.MemoryMB == nil {
		return result, errors.New("bead machine resources must be resolved before provisioning")
	}
	id, found, err := p.InspectMachine(ctx, installationID, machineID, machineProvisioning, "")
	if err != nil {
		return result, err
	}
	if found {
		result.ProviderResourceID = id
	} else if firstAttempt && !p.createAttempted {
		p.createAttempted = true
		created, err := p.api.CreateSandbox(ctx, createSandboxRequest{
			TemplateID: options.template(),
			Kind:       "persistent",
			CPUCount:   *machineProvisioning.CPU,
			MemoryMB:   *machineProvisioning.MemoryMB,
			Metadata:   metadata,
			EnvVars:    machineEnv,
		})
		if err != nil {
			status := apiStatusCode(err)
			if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || isDialError(err) {
				p.createAttempted = false
			} else if status >= 400 && status < 500 && status != http.StatusRequestTimeout {
				return result, fmt.Errorf("create bead sandbox: %w: %w", err, providers.ErrPermanent)
			}
			return result, fmt.Errorf("create bead sandbox: %w", err)
		}
		result.ProviderResourceID = created.SandboxID
	} else {
		id, found, err := p.InspectMachine(ctx, installationID, machineID, machineProvisioning, "")
		if err != nil {
			return result, err
		}
		if !found {
			return result, errors.New(
				"bead create outcome is unknown; waiting to discover the existing sandbox instead of creating a duplicate",
			)
		}
		result.ProviderResourceID = id
	}
	if result.ProviderResourceID == "" {
		return providers.ProvisionMachineResult{}, errors.New("bead sandbox is missing its id")
	}
	target, found, err := p.api.GetSandbox(ctx, result.ProviderResourceID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, fmt.Errorf("bead sandbox %q was not found", result.ProviderResourceID)
	}
	if !ownedBy(target, metadata) {
		return result, fmt.Errorf("bead sandbox %q does not have the expected ownership metadata", target.SandboxID)
	}
	if machineProvisioning.CPU == nil || machineProvisioning.MemoryMB == nil ||
		target.CPUCount != *machineProvisioning.CPU || target.MemoryMB != *machineProvisioning.MemoryMB {
		return result, fmt.Errorf(
			"bead sandbox %q has cpu=%d memory_mb=%d, not the resolved machine size: %w",
			target.SandboxID,
			target.CPUCount,
			target.MemoryMB,
			providers.ErrPermanent,
		)
	}
	if target.State == sandboxStatePaused {
		if err := p.api.ResumeSandbox(ctx, target.SandboxID); err != nil {
			return result, err
		}
		target, found, err = p.api.GetSandbox(ctx, target.SandboxID)
		if err != nil {
			return result, err
		}
		if !found {
			return result, errors.New("bead sandbox disappeared after resume")
		}
	}
	processes, err := p.api.ListProcesses(ctx, target)
	if err != nil {
		return result, fmt.Errorf("list bead sandbox processes: %w", err)
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
			return "", false, errors.New("multiple bead sandboxes have the same Omnara ownership")
		}
		target = owned[0]
	} else {
		var found bool
		target, found, err = p.api.GetSandbox(ctx, providerResourceID)
		if err != nil || !found {
			return "", false, err
		}
	}
	if target.SandboxID == "" || (providerResourceID != "" && target.SandboxID != providerResourceID) ||
		!ownedBy(target, metadata) {
		return "", false, fmt.Errorf("bead sandbox %q does not have the expected ownership metadata", target.SandboxID)
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
	if err != nil {
		return err
	}
	if !found {
		// A lifecycle request can briefly hide a resource by ID before its outcome is known.
		resourceID, found, err = p.InspectMachine(ctx, installationID, machineID, machineProvisioning, "")
		if err != nil || !found {
			return err
		}
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
	allocation, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		installationLabel: installationOwner, machineLabel: machineOwner, "omnara-allocation": allocation,
	}, nil
}

func ownedBy(target sandbox, metadata map[string]string) bool {
	return target.Metadata[installationLabel] == metadata[installationLabel] &&
		target.Metadata[machineLabel] == metadata[machineLabel] &&
		target.Metadata["omnara-allocation"] == metadata["omnara-allocation"]
}

var _ providers.MachineWaker = (*provider)(nil)

func (p *provider) WakeMachine(ctx context.Context, input providers.WakeMachineInput) error {
	if input.ProviderResourceID == "" {
		return errors.New("provider resource id is required")
	}
	current, found, err := p.api.GetSandbox(ctx, input.ProviderResourceID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("bead sandbox not found")
	}
	if current.State == sandboxStateRunning {
		return nil
	}
	return p.api.ResumeSandbox(ctx, current.SandboxID)
}
