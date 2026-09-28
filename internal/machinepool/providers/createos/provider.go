package createos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"slices"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const provisioningTimeout = 2 * time.Minute

type provider struct {
	api          apiClient
	omnaraAPIURL string
}

func (*provider) ProvisioningTimeout() time.Duration { return provisioningTimeout }

func (p *provider) PrepareProvisioning(
	ctx context.Context,
	provisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	options, err := parseProviderOptions(provisioning.ProviderOptions)
	if err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	if provisioning.CPU != nil && provisioning.MemoryMB != nil {
		return executionstore.MachineResourceFacts{CPU: provisioning.CPU, MemoryMB: provisioning.MemoryMB}, nil
	}
	shapes, err := p.api.ListShapes(ctx)
	if err != nil {
		return executionstore.MachineResourceFacts{}, fmt.Errorf("list createos shapes: %w", err)
	}
	for _, candidate := range shapes {
		if candidate.ID == options.Shape {
			if candidate.VCPU <= 0 || candidate.MemMiB <= 0 {
				return executionstore.MachineResourceFacts{}, errors.New("createos shape has invalid resources")
			}
			return executionstore.MachineResourceFacts{CPU: &candidate.VCPU, MemoryMB: &candidate.MemMiB}, nil
		}
	}
	return executionstore.MachineResourceFacts{}, fmt.Errorf("createos shape %q was not found", options.Shape)
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	provisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
) (providers.ProvisionMachineResult, error) {
	options, err := parseProviderOptions(provisioning.ProviderOptions)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	name, err := allocationName(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	target, found, err := p.findByName(ctx, name)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if !found {
		env, err := providers.BuildManagedMachineEnv(p.omnaraAPIURL, machineToken, options.StartupScript, machineEnv)
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
		env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
		target, err = p.api.CreateSandbox(ctx, createSandboxRequest{
			Shape:  options.Shape,
			RootFS: options.RootFS,
			Name:   name,
			Region: options.Region,
			Envs:   env,
		})
		if err != nil {
			target, found, _ = p.findByName(ctx, name)
			if !found {
				return providers.ProvisionMachineResult{}, err
			}
		} else {
			// The create response intentionally omits lifecycle status and region.
			// Resolve the authoritative public view before validating/adopting it.
			createdID := target.ID
			target, found, err = p.api.GetSandbox(ctx, target.ID)
			if err != nil {
				return providers.ProvisionMachineResult{ProviderResourceID: createdID}, err
			}
			if !found {
				return providers.ProvisionMachineResult{}, errors.New("created createos sandbox was not found")
			}
		}
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if target.ID == "" || target.Name != name {
		return result, errors.New("createos sandbox does not match expected allocation")
	}
	if target.Shape != options.Shape || target.RootFS != options.RootFS || target.Region != options.Region {
		return result, errors.New("createos sandbox configuration does not match provisioning intent")
	}
	if target.Status != sandboxStatusRunning {
		return result, fmt.Errorf("createos sandbox %q is not running (status %q)", target.ID, target.Status)
	}
	processes, err := p.api.ListProcesses(ctx, target.ID)
	if err != nil {
		return result, err
	}
	if !slices.ContainsFunc(processes, func(process process) bool {
		return !process.LeaderExited && (process.State == "starting" || process.State == "running")
	}) {
		args := providers.ManagedDaemonLauncherArgs()
		created, err := p.api.CreateProcess(ctx, target.ID, createProcessRequest{Command: args[0], Args: args[1:]})
		if err != nil {
			return result, err
		}
		if created.ID == "" {
			return result, errors.New("createos daemon process response is missing process id")
		}
	}
	return result, nil
}

func (p *provider) findByName(ctx context.Context, name string) (sandbox, bool, error) {
	for offset := 0; ; offset += 500 {
		items, total, err := p.api.ListSandboxes(ctx, 500, offset)
		if err != nil {
			return sandbox{}, false, err
		}
		matches := make([]sandbox, 0, 1)
		for _, item := range items {
			if item.Name == name && item.Status != sandboxStatusDestroyed && item.Status != sandboxStatusFailed {
				matches = append(matches, item)
			}
		}
		if len(matches) > 1 {
			return sandbox{}, false, fmt.Errorf("multiple createos sandboxes have allocation name %q", name)
		}
		if len(matches) == 1 {
			return matches[0], true, nil
		}
		if offset+len(items) >= total || len(items) == 0 {
			return sandbox{}, false, nil
		}
	}
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	resourceID string,
) (string, bool, error) {
	name, err := allocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	if resourceID == "" {
		target, found, err := p.findByName(ctx, name)
		return target.ID, found, err
	}
	target, found, err := p.api.GetSandbox(ctx, resourceID)
	if err != nil || !found {
		return "", found, err
	}
	if target.Name != name {
		return "", false, fmt.Errorf("createos sandbox %q does not have expected allocation name", resourceID)
	}
	return target.ID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	provisioning executionstore.MachineProvisioningConfig,
	resourceID string,
) error {
	if strings.TrimSpace(resourceID) == "" {
		return errors.New("provider resource id is required")
	}
	id, found, err := p.InspectMachine(ctx, installationID, machineID, provisioning, resourceID)
	if err != nil || !found {
		return err
	}
	return p.api.DeleteSandbox(ctx, id)
}

func allocationName(installationID, machineID uuid.UUID) (string, error) {
	canonical, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return "omnara-" + hex.EncodeToString(sum[:])[:15], nil
}
