package daytona

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	daemonSessionName   = "omnara-daemon"
	provisioningTimeout = time.Minute

	// Daytona sandbox states follow https://www.daytona.io/docs/en/go-sdk/daytona/#constants.
	sandboxStateCreating         sandboxState = "creating"
	sandboxStateRestoring        sandboxState = "restoring"
	sandboxStateDestroyed        sandboxState = "destroyed"
	sandboxStateDestroying       sandboxState = "destroying"
	sandboxStateStarted          sandboxState = "started"
	sandboxStateStopped          sandboxState = "stopped"
	sandboxStateStarting         sandboxState = "starting"
	sandboxStateStopping         sandboxState = "stopping"
	sandboxStateError            sandboxState = "error"
	sandboxStateBuildFailed      sandboxState = "build_failed"
	sandboxStatePendingBuild     sandboxState = "pending_build"
	sandboxStateBuildingSnapshot sandboxState = "building_snapshot"
	sandboxStateUnknown          sandboxState = "unknown"
	sandboxStatePullingSnapshot  sandboxState = "pulling_snapshot"
	sandboxStateArchived         sandboxState = "archived"
	sandboxStateArchiving        sandboxState = "archiving"
	sandboxStateResizing         sandboxState = "resizing"
	sandboxStateSnapshotting     sandboxState = "snapshotting"
	sandboxStateForking          sandboxState = "forking"
	sandboxStatePausing          sandboxState = "pausing"
	sandboxStatePaused           sandboxState = "paused"
	sandboxStateResuming         sandboxState = "resuming"
	snapshotStateActive                       = "active"
)

type sandboxState string

type provider struct {
	api          apiClient
	omnaraAPIURL string
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
	snapshot, err := p.api.GetSnapshot(ctx, options.Snapshot)
	if err != nil {
		var apiErr apiError
		if !errors.As(err, &apiErr) || apiErr.StatusCode == http.StatusTooManyRequests ||
			apiErr.StatusCode >= http.StatusInternalServerError {
			return executionstore.MachineResourceFacts{}, fmt.Errorf(
				"get daytona snapshot %q: %w: %w",
				options.Snapshot,
				storeerr.ErrMachineProviderUnavailable,
				err,
			)
		}
		return executionstore.MachineResourceFacts{}, fmt.Errorf(
			"get daytona snapshot %q: %w",
			options.Snapshot,
			err,
		)
	}
	if strings.TrimSpace(snapshot.Name) != options.Snapshot {
		return executionstore.MachineResourceFacts{}, fmt.Errorf(
			"daytona snapshot %q must be configured by name",
			options.Snapshot,
		)
	}
	if state(snapshot.State) != snapshotStateActive {
		return executionstore.MachineResourceFacts{}, fmt.Errorf(
			"daytona snapshot %q is not active with state %q",
			options.Snapshot,
			snapshot.State,
		)
	}
	if len(snapshot.RegionIDs) > 0 && !slices.Contains(snapshot.RegionIDs, options.Target) {
		return executionstore.MachineResourceFacts{}, fmt.Errorf(
			"daytona snapshot %q is not available in target %q",
			options.Snapshot,
			options.Target,
		)
	}
	if snapshot.GPU != 0 {
		return executionstore.MachineResourceFacts{}, errors.New("daytona GPU snapshots are not supported")
	}
	cpu, err := positiveWholeNumber(snapshot.CPU, "snapshot cpu")
	if err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	memoryMB, err := positiveWholeNumber(snapshot.Memory*1024, "snapshot memory_mb")
	if err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	return executionstore.MachineResourceFacts{CPU: &cpu, MemoryMB: &memoryMB}, nil
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
		p.omnaraAPIURL,
		machineToken,
		options.StartupScript,
		machineEnv,
	)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	request := createSandboxRequest{
		Name:               name,
		Snapshot:           options.Snapshot,
		Target:             options.Target,
		Env:                env,
		Labels:             map[string]string{"omnara-machine": name},
		AutoStopInterval:   0,
		AutoDeleteInterval: -1,
	}
	api := p.api
	target, err := api.CreateSandbox(ctx, request)
	if isConflict(err) {
		existing, found, getErr := api.GetSandbox(ctx, name)
		if getErr != nil {
			return providers.ProvisionMachineResult{}, getErr
		}
		if !found {
			return providers.ProvisionMachineResult{}, fmt.Errorf(
				"daytona sandbox %q create conflicted but the sandbox was not found",
				name,
			)
		}
		target = existing
	} else if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	var result providers.ProvisionMachineResult
	for {
		if !sandboxOwnedBy(target, name) {
			return result, fmt.Errorf("daytona sandbox %q does not have the expected ownership label", name)
		}
		if target.ID != "" {
			if result.ProviderResourceID != "" && result.ProviderResourceID != target.ID {
				return result, fmt.Errorf(
					"daytona sandbox %q changed id from %q to %q",
					name,
					result.ProviderResourceID,
					target.ID,
				)
			}
			result.ProviderResourceID = target.ID
		}
		if normalizeSandboxState(target.State) == sandboxStateStarted {
			break
		}
		if sandboxReplaceable(target.State) {
			if err := api.DeleteSandbox(ctx, name); err != nil {
				return result, err
			}
			return providers.ProvisionMachineResult{}, fmt.Errorf(
				"daytona sandbox %q was deleted; provisioning must be retried: %w",
				name,
				providers.ErrResourceReplaced,
			)
		}
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("wait for daytona sandbox %q to start: %w", name, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
		refreshed, found, err := api.GetSandbox(ctx, name)
		if err != nil {
			return result, err
		}
		if !found {
			return result, fmt.Errorf("daytona sandbox %q disappeared while starting", name)
		}
		target = refreshed
	}
	if result.ProviderResourceID == "" {
		return result, errors.New("daytona sandbox is missing its id")
	}
	if err := validateSandboxResources(target, machineProvisioning, options.Target); err != nil {
		return result, err
	}
	if err := ensureDaemonSession(ctx, api, target, options.StartupScript); err != nil {
		return result, err
	}
	return result, nil
}

func ensureDaemonSession(
	ctx context.Context,
	api apiClient,
	target sandbox,
	startupScript string,
) error {
	currentSession, found, err := api.GetSession(ctx, target, daemonSessionName)
	if err != nil {
		return err
	}
	if found {
		for _, cmd := range currentSession.Commands {
			if cmd.ExitCode == nil {
				return nil
			}
		}
		if err := api.DeleteSession(ctx, target, daemonSessionName); err != nil {
			return err
		}
	}
	if err := api.CreateSession(ctx, target, daemonSessionName); err != nil && !isConflict(err) {
		return err
	}
	response, err := api.ExecuteSessionCommand(
		ctx,
		target,
		daemonSessionName,
		sessionExecuteRequest{
			Command:  providers.ManagedBootScript(),
			RunAsync: true,
		},
	)
	if err != nil {
		return err
	}
	if response.CommandID == "" {
		return errors.New("daytona daemon session response is missing command id")
	}
	if response.ExitCode != nil {
		return fmt.Errorf("daytona daemon command exited with status %d", *response.ExitCode)
	}
	return nil
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
	target, found, err := p.api.GetSandbox(ctx, lookup)
	if err != nil || !found {
		return "", false, err
	}
	if !sandboxOwnedBy(target, expectedName) {
		return "", false, fmt.Errorf("daytona sandbox %q does not have the expected ownership label", lookup)
	}
	if target.ID == "" {
		return "", false, fmt.Errorf("daytona sandbox %q is missing its id", lookup)
	}
	return target.ID, true, nil
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
	return p.api.DeleteSandbox(ctx, resourceID)
}

func sandboxOwnedBy(target sandbox, name string) bool {
	return target.Name == name && target.Labels["omnara-machine"] == name
}

func sandboxReplaceable(value sandboxState) bool {
	return slices.Contains(
		[]sandboxState{
			sandboxStateArchived,
			sandboxStateBuildFailed,
			sandboxStateDestroyed,
			sandboxStateError,
			sandboxStateStopped,
		},
		normalizeSandboxState(value),
	)
}

func normalizeSandboxState(value sandboxState) sandboxState {
	return sandboxState(state(string(value)))
}

func state(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateSandboxResources(
	target sandbox,
	machineProvisioning executionstore.MachineProvisioningConfig,
	expectedTarget string,
) error {
	if target.Target != expectedTarget {
		return fmt.Errorf(
			"daytona sandbox target %q does not match expected target %q",
			target.Target,
			expectedTarget,
		)
	}
	cpu, err := positiveWholeNumber(target.CPU, "sandbox cpu")
	if err != nil {
		return err
	}
	memoryMB, err := positiveWholeNumber(target.Memory*1024, "sandbox memory_mb")
	if err != nil {
		return err
	}
	if machineProvisioning.CPU == nil {
		return errors.New("daytona resolved machine cpu is required")
	}
	if cpu != *machineProvisioning.CPU || memoryMB != *machineProvisioning.MemoryMB {
		return fmt.Errorf(
			"daytona sandbox resources cpu=%d memory_mb=%d do not match resolved machine resources cpu=%d memory_mb=%d",
			cpu,
			memoryMB,
			*machineProvisioning.CPU,
			*machineProvisioning.MemoryMB,
		)
	}
	return nil
}
