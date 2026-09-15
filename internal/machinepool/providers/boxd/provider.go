package boxd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	// A fresh boxd machine boots in a few seconds, but org-scoped creates go
	// through the control plane's consensus path and the in-VM agent needs a
	// moment before exec is reachable.
	provisioningTimeout    = 90 * time.Second
	vmStatusPollInterval   = 500 * time.Millisecond
	bootstrapRetryInterval = time.Second
	mebibyte               = 1024 * 1024
	// reasonOutputLimit bounds how much bootstrap stderr reaches the lifecycle
	// reason message that surfaces in the UI.
	reasonOutputLimit = 512
	// capacityRetryDelay is the hint attached to a capacity refusal. It is
	// longer than any provisioning deadline on purpose, so the caller's
	// immediate retries stop and reconciliation's backoff owns the retry.
	capacityRetryDelay = 10 * time.Minute

	// boxd machine statuses follow the CLI's `machine get` output.
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

// PrepareProvisioning resolves the machine size. A snapshot fixes it, because
// boxd restores at the size the snapshot was captured at, so a configured
// size must agree with it. Otherwise a configured size wins, then the boxd
// org default.
func (p *provider) PrepareProvisioning(
	ctx context.Context,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineResourceFacts, error) {
	options, err := parseProviderOptions(machineProvisioning.ProviderOptions)
	if err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	if err := validateConfiguredSize(
		"boxd machine config",
		machineProvisioning.CPU,
		machineProvisioning.MemoryMB,
	); err != nil {
		return executionstore.MachineResourceFacts{}, err
	}
	configured := configuredSizeFacts(machineProvisioning)
	if options.Snapshot != "" {
		facts, err := p.snapshotResourceFacts(ctx, options.Snapshot)
		if err != nil {
			return executionstore.MachineResourceFacts{}, err
		}
		if configured != nil && (*configured.CPU != *facts.CPU || *configured.MemoryMB != *facts.MemoryMB) {
			return executionstore.MachineResourceFacts{}, fmt.Errorf(
				"boxd machine config cpu=%d memory_mb=%d does not match snapshot %q, "+
					"which restores at cpu=%d memory_mb=%d; omit the size or match it",
				*configured.CPU,
				*configured.MemoryMB,
				options.Snapshot,
				*facts.CPU,
				*facts.MemoryMB,
			)
		}
		return facts, nil
	}
	if configured != nil {
		return *configured, nil
	}
	size, err := p.api.GetOrgMachineDefaults(ctx)
	if err != nil {
		return executionstore.MachineResourceFacts{}, classifyPreparationError(
			"get boxd org machine defaults",
			err,
		)
	}
	// boxd reports the effective default, which is the org quota ceiling when
	// no explicit default is stored, so the size is always usable when present.
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

// configuredSizeFacts resolves a configured size to its full class, or nil
// when the pool leaves the size to the snapshot or the org default.
func configuredSizeFacts(
	machineProvisioning executionstore.MachineProvisioningConfig,
) *executionstore.MachineResourceFacts {
	var facts executionstore.MachineResourceFacts
	switch {
	case machineProvisioning.CPU != nil && machineProvisioning.MemoryMB != nil:
		facts = resourceFacts(*machineProvisioning.CPU, *machineProvisioning.MemoryMB)
	case machineProvisioning.CPU != nil:
		facts = resourceFacts(*machineProvisioning.CPU, supportedSizeClasses[*machineProvisioning.CPU])
	case machineProvisioning.MemoryMB != nil:
		cpu, _ := sizeClassForMemory(*machineProvisioning.MemoryMB)
		facts = resourceFacts(cpu, *machineProvisioning.MemoryMB)
	default:
		return nil
	}
	return &facts
}

func resourceFacts(cpu, memoryMB int) executionstore.MachineResourceFacts {
	return executionstore.MachineResourceFacts{CPU: &cpu, MemoryMB: &memoryMB}
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
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
	// With a sleep window the daemon parks itself when idle and announces it,
	// which stops the heartbeat and lets boxd's idle clock run down. The daemon
	// wakes again on the wall-clock jump a resume produces, so boxd needs no
	// wake listener exposed.
	//
	// No sleep platform is set on purpose. boxd's idle clock is driven by the
	// guest traffic the daemon already produces, so parking and resuming need
	// no platform call, and an unset name is the one value every released
	// daemon understands. Naming a platform a daemon does not know is fatal to
	// it, so a new name would strand pools until every machine image caught up.
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
	}
	payload, err := bootPayload(env)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	api := p.api
	// Names are unique across boxd, so an existing machine with the
	// allocation name is this machine from an earlier attempt.
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
		// Create responses omit the effective size; read it back.
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
	// Omnara stores this as the machine's sandbox url and treats its presence
	// as the machine being wake capable, which is what lets the daemon park.
	result.SandboxURL = target.url()
	if result.SandboxURL == "" {
		return result, fmt.Errorf("boxd machine %q is missing its access domain", name)
	}
	if err := ensureDaemon(ctx, api, target, payload); err != nil {
		return result, err
	}
	return result, nil
}

// describeCreateFailure turns a refused create into the error the operator
// sees on the machine. A capacity refusal carries boxd's own sentence, which
// names the limit and what to do about it, wrapped so the caller's retry
// loop stops instead of burning its budget on a condition that only clears
// when someone frees a machine.
//
// A refusal boxd answered synchronously, as opposed to a transport failure,
// also proves no machine was created, and says so with ErrResourceNotCreated
// so cleanup can finalize at once rather than hold the machine for the
// grace period that guards a create whose outcome was never observed.
func describeCreateFailure(name string, err error) error {
	if isCapacityRefusal(err) {
		var apiErr apiError
		message := err.Error()
		if errors.As(err, &apiErr) && apiErr.Message != "" {
			message = apiErr.Message
		}
		return providers.WithRetryDelay(
			fmt.Errorf(
				"create boxd machine %q: %w: %w: %s",
				name,
				ErrCapacityRefused,
				providers.ErrResourceNotCreated,
				message,
			),
			capacityRetryDelay,
		)
	}
	if isRejectedRequest(err) {
		return fmt.Errorf("create boxd machine %q: %w: %w", name, providers.ErrResourceNotCreated, err)
	}
	return fmt.Errorf("create boxd machine %q: %w", name, err)
}

// ensureDaemon starts the managed daemon bootstrap through boxd exec. The
// launcher is idempotent, so the exec is retried while the freshly booted
// machine's agent is still unreachable.
func ensureDaemon(ctx context.Context, api apiClient, target vm, payload []byte) error {
	result, err := execWithRetry(ctx, api, target.ID, launcherCommand(), payload)
	if err != nil {
		return fmt.Errorf("start boxd daemon bootstrap on machine %q: %w", target.Name, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"boxd daemon bootstrap on machine %q exited with status %d: %s",
			target.Name,
			result.ExitCode,
			tailForReason(result.Stderr),
		)
	}
	return nil
}

// execWithRetry runs a command in the machine, retrying while the guest agent
// is unreachable, which is the case for a moment after boot and after a resume.
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

// tailForReason keeps the end of a command's output, which is where a shell
// reports what failed, and bounds it for the lifecycle reason message. The
// reason is stored as Postgres text, which refuses NUL and invalid UTF-8, so
// the output is made valid first and the cut lands on a character boundary;
// a bootstrap failure must never become a failure to record the failure.
func tailForReason(output string) string {
	output = strings.ToValidUTF8(strings.TrimSpace(output), "?")
	output = strings.ReplaceAll(output, "\x00", "?")
	if len(output) <= reasonOutputLimit {
		return output
	}
	cut := len(output) - reasonOutputLimit
	for cut < len(output) && !utf8.RuneStart(output[cut]) {
		cut++
	}
	return "..." + output[cut:]
}

// wakePokeCommand connects to the daemon's wake listener from inside the
// machine. The daemon answers 204 and leaves its parked state; curl's exit
// status reports whether anything was listening.
func wakePokeCommand() string {
	return "curl -fsS -o /dev/null --max-time 3 http://127.0.0.1:" +
		strconv.Itoa(daemonprotocol.WakeListenerPort) + "/"
}

// WakeMachine reconnects a parked daemon. The poke is delivered as an exec
// because that is the one signal that reaches the daemon in every state it can
// be in: parked on a machine boxd has not suspended yet, where a resume would
// be a no-op, and parked on a suspended or hibernated machine, which boxd
// brings back before running the command. Reaching the listener through the
// authenticated API also keeps it off the machine's public address.
//
// The exec is retried while the guest agent is unreachable, and the poke is
// idempotent, so a wake retried after an ambiguous failure is safe.
func (p *provider) WakeMachine(ctx context.Context, input providers.WakeMachineInput) error {
	if input.ProviderResourceID == "" {
		return errors.New("boxd wake requires a provider resource id")
	}
	result, err := execWithRetry(ctx, p.api, input.ProviderResourceID, wakePokeCommand(), nil)
	if err != nil {
		return fmt.Errorf("wake boxd machine %q: %w", input.ProviderResourceID, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf(
			"wake boxd machine %q: the daemon wake listener did not answer: %s",
			input.ProviderResourceID,
			tailForReason(result.Stderr),
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

// vmUsable reports statuses where exec succeeds: boxd wakes a suspended or
// hibernated machine on its first exec.
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
	if machineProvisioning.CPU == nil || machineProvisioning.MemoryMB == nil {
		return errors.New("boxd resolved machine size is required")
	}
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
