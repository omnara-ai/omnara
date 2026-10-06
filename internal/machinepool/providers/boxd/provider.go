package boxd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	provisioningTimeout    = 90 * time.Second
	vmStatusPollInterval   = 500 * time.Millisecond
	bootstrapRetryInterval = time.Second
	mebibyte               = 1024 * 1024
	wakeTimeout            = 10 * time.Second
	runtimeListTimeout     = 10 * time.Second

	vmStatusPending     vmStatus = "pending"
	vmStatusStarting    vmStatus = "starting"
	vmStatusRunning     vmStatus = "running"
	vmStatusStopping    vmStatus = "stopping"
	vmStatusStopped     vmStatus = "stopped"
	vmStatusSuspended   vmStatus = "suspended"
	vmStatusStandby     vmStatus = "standby"
	vmStatusHibernating vmStatus = "hibernating"
	vmStatusHibernated  vmStatus = "hibernated"
	vmStatusRebooting   vmStatus = "rebooting"
	vmStatusMigrating   vmStatus = "migrating"
	vmStatusDestroying  vmStatus = "destroying"
	vmStatusDestroyed   vmStatus = "destroyed"
	vmStatusFailed      vmStatus = "failed"

	snapshotStatusReady = "ready"
)

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
	if options.Snapshot != "" {
		return p.snapshotResourceFacts(ctx, options.Snapshot)
	}
	size, err := p.api.GetOrgMachineDefaults(ctx)
	if err != nil {
		return executionstore.MachineResourceFacts{}, classifyPreparationError(
			"get boxd org machine defaults",
			err,
		)
	}
	return machineSizeFacts("boxd org default", size.VCPU, size.MemoryBytes)
}

func (p *provider) snapshotResourceFacts(
	ctx context.Context,
	snapshotRef string,
) (executionstore.MachineResourceFacts, error) {
	snapshot, found, err := p.api.GetSnapshot(ctx, snapshotRef)
	if err != nil {
		return executionstore.MachineResourceFacts{}, classifyPreparationError(
			fmt.Sprintf("get boxd snapshot %q", snapshotRef),
			err,
		)
	}
	if !found {
		return executionstore.MachineResourceFacts{}, fmt.Errorf("boxd snapshot %q was not found", snapshotRef)
	}
	if normalizeStatus(snapshot.Status) != snapshotStatusReady {
		return executionstore.MachineResourceFacts{}, fmt.Errorf(
			"boxd snapshot %q is not ready with status %q",
			snapshotRef,
			snapshot.Status,
		)
	}
	return machineSizeFacts(fmt.Sprintf("boxd snapshot %q", snapshotRef), snapshot.VCPU, snapshot.MemoryBytes)
}

func classifyPreparationError(what string, err error) error {
	if isTransient(err) {
		return fmt.Errorf("%s: %w: %w", what, storeerr.ErrMachineProviderUnavailable, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func machineSizeFacts(what string, vcpu int, memoryBytes uint64) (executionstore.MachineResourceFacts, error) {
	if vcpu <= 0 {
		return executionstore.MachineResourceFacts{}, fmt.Errorf("%s reports no vcpu", what)
	}
	if memoryBytes == 0 || memoryBytes%mebibyte != 0 || memoryBytes/mebibyte > math.MaxInt32 {
		return executionstore.MachineResourceFacts{}, fmt.Errorf("%s reports an unusable memory size", what)
	}
	return resourceFacts(vcpu, int(memoryBytes/mebibyte)), nil
}

func resourceFacts(cpu, memoryMB int) executionstore.MachineResourceFacts {
	return executionstore.MachineResourceFacts{CPU: &cpu, MemoryMB: &memoryMB}
}

func (*provider) ValidateMachineConfig(executionstore.MachineProvisioningConfig, map[string]string) error {
	return nil
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
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
	env, err := providers.BuildManagedMachineEnv(
		p.omnaraAPIURL,
		machineToken,
		options.StartupScript,
		machineEnv,
	)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	script := providers.ManagedBootScript()
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		script = bootstrapKeepAwakeScript + script
	}
	api := p.api
	target, found, err := api.GetVM(ctx, name)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if !found {
		request := createVMRequest{
			Name:                   name,
			Snapshot:               options.Snapshot,
			AutoSuspendTimeoutSecs: options.autoSuspendSecs(),
		}
		if options.Snapshot == "" {
			request.VCPU = *machineProvisioning.CPU
			request.MemoryBytes = uint64(*machineProvisioning.MemoryMB) * mebibyte
		}
		created, createErr := api.CreateVM(ctx, request)
		if createErr != nil {
			existing, found, getErr := api.GetVM(ctx, name)
			if getErr != nil || !found {
				return providers.ProvisionMachineResult{}, describeCreateFailure(name, createErr)
			}
			created = existing
		}
		target = created
	}
	var result providers.ProvisionMachineResult
	for {
		if !vmOwnedBy(target, name) {
			return result, fmt.Errorf("boxd machine %q does not have the expected allocation name", name)
		}
		if target.ID != "" {
			if result.ProviderResourceID != "" && result.ProviderResourceID != target.ID {
				return result, fmt.Errorf(
					"boxd machine %q changed id from %q to %q",
					name,
					result.ProviderResourceID,
					target.ID,
				)
			}
			result.ProviderResourceID = target.ID
		}
		if vmUsable(target.Status) {
			break
		}
		if vmReplaceable(target.Status) {
			if err := api.DestroyVM(ctx, name); err != nil {
				return result, err
			}
			return providers.ProvisionMachineResult{}, fmt.Errorf(
				"boxd machine %q was deleted; provisioning must be retried: %w",
				name,
				providers.ErrResourceReplaced,
			)
		}
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("wait for boxd machine %q to start: %w", name, ctx.Err())
		case <-time.After(vmStatusPollInterval):
		}
		refreshed, found, err := api.GetVM(ctx, name)
		if err != nil {
			return result, err
		}
		if !found {
			return result, fmt.Errorf("boxd machine %q disappeared while starting", name)
		}
		target = refreshed
	}
	if result.ProviderResourceID == "" {
		return result, errors.New("boxd machine is missing its id")
	}
	if target.VCPU == 0 || target.MemoryBytes == 0 {
		refreshed, found, err := api.GetVM(ctx, result.ProviderResourceID)
		if err != nil {
			return result, err
		}
		if !found {
			return result, fmt.Errorf("boxd machine %q disappeared after starting", name)
		}
		target = refreshed
	}
	if err := validateVMResources(target, machineProvisioning); err != nil {
		return result, err
	}
	if options.SleepAfterMS > 0 {
		result.SandboxURL = target.url()
		if result.SandboxURL == "" {
			return result, fmt.Errorf("boxd machine %q is missing its access domain", name)
		}
	}
	if err := ensureDaemon(ctx, api, target, bootPayload(env, script)); err != nil {
		return result, err
	}
	return result, nil
}

func describeCreateFailure(name string, err error) error {
	if isRejectedRequest(err) {
		return fmt.Errorf("create boxd machine %q: %w: %w", name, err, providers.ErrPermanent)
	}
	return fmt.Errorf("create boxd machine %q: %w", name, err)
}

func ensureDaemon(ctx context.Context, api apiClient, target vm, payload []byte) error {
	result, err := execWithRetry(ctx, api, target.ID, launcherCommand(), payload)
	if err != nil {
		return fmt.Errorf("start boxd daemon bootstrap on machine %q: %w", target.Name, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"boxd daemon bootstrap on machine %q exited with status %d",
			target.Name,
			result.ExitCode,
		)
	}
	return nil
}

func execWithRetry(
	ctx context.Context,
	api apiClient,
	resourceID, command string,
	stdin []byte,
) (execResult, error) {
	for {
		result, err := api.Exec(ctx, resourceID, command, stdin)
		if err == nil {
			return result, nil
		}
		if !isTransient(err) && !isCode(err, codes.FailedPrecondition) {
			return execResult{}, err
		}
		select {
		case <-ctx.Done():
			return execResult{}, fmt.Errorf("%w: %w", ctx.Err(), err)
		case <-time.After(bootstrapRetryInterval):
		}
	}
}

func wakePokeCommand() string {
	return "curl -fsS -o /dev/null --max-time 3 http://127.0.0.1:" +
		strconv.Itoa(daemonprotocol.WakeListenerPort) + "/"
}

func (p *provider) WakeMachine(ctx context.Context, input providers.WakeMachineInput) error {
	if input.ProviderResourceID == "" {
		return errors.New("boxd wake requires a provider resource id")
	}
	ctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	result, err := execWithRetry(ctx, p.api, input.ProviderResourceID, wakePokeCommand(), nil)
	if err != nil {
		return fmt.Errorf("wake boxd machine %q: %w", input.ProviderResourceID, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"wake boxd machine %q: the daemon wake listener exited with status %d",
			input.ProviderResourceID,
			result.ExitCode,
		)
	}
	return nil
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
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
	target, found, err := p.api.GetVM(ctx, lookup)
	if err != nil || !found {
		return "", false, err
	}
	if !vmOwnedBy(target, expectedName) {
		return "", false, fmt.Errorf("boxd machine %q does not have the expected allocation name", lookup)
	}
	if target.ID == "" {
		return "", false, fmt.Errorf("boxd machine %q is missing its id", lookup)
	}
	return target.ID, true, nil
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
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
	return p.api.DestroyVM(ctx, resourceID)
}

func vmOwnedBy(target vm, name string) bool {
	return target.Name == name
}

func vmUsable(value vmStatus) bool {
	switch normalizeVMStatus(value) {
	case vmStatusRunning, vmStatusSuspended, vmStatusStandby, vmStatusHibernated:
		return true
	default:
		return false
	}
}

func vmReplaceable(value vmStatus) bool {
	switch normalizeVMStatus(value) {
	case vmStatusStopped, vmStatusFailed, vmStatusDestroying, vmStatusDestroyed:
		return true
	default:
		return false
	}
}

func normalizeVMStatus(value vmStatus) vmStatus {
	return vmStatus(normalizeStatus(string(value)))
}

func normalizeStatus(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateVMResources(
	target vm,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if target.VCPU <= 0 || target.MemoryBytes == 0 || target.MemoryBytes%mebibyte != 0 {
		return fmt.Errorf("boxd machine %q reports an unusable size", target.Name)
	}
	memoryMB := target.MemoryBytes / mebibyte
	if target.VCPU != *machineProvisioning.CPU || memoryMB != uint64(*machineProvisioning.MemoryMB) {
		return fmt.Errorf(
			"boxd machine resources cpu=%d memory_mb=%d do not match resolved machine resources cpu=%d memory_mb=%d",
			target.VCPU,
			memoryMB,
			*machineProvisioning.CPU,
			*machineProvisioning.MemoryMB,
		)
	}
	return nil
}
