package createos

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	provisioningTimeout     = 2 * time.Minute
	sandboxListPageSize     = 500
	maxSandboxEnvEntries    = 64
	maxSandboxEnvValueBytes = 4096
	maxSandboxEnvBytes      = 64 * 1024
	sandboxEnvEntryOverhead = 4
	wakeTimeout             = time.Minute
	pollDelay               = time.Second
	wakeSettlePolls         = 40
)

//go:embed guest_dev_fixup.sh
var guestDevFixupScript string

var sandboxEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	wakeListenAddr  = "127.0.0.1:" + strconv.Itoa(daemonprotocol.WakeListenerPort)
	wakePokeCommand = commandRequest{
		Command: "curl",
		Args:    []string{"-q", "--noproxy", "*", "-fsS", "-m", "5", "http://" + wakeListenAddr + "/"},
	}
)

var liveSandboxStatuses = [...]sandboxStatus{
	sandboxStatusRunning,
	sandboxStatusCreating,
	sandboxStatusPausing,
	sandboxStatusPaused,
	sandboxStatusResuming,
	sandboxStatusForking,
	sandboxStatusError,
	sandboxStatusDestroying,
}

type provider struct {
	api          apiClient
	omnaraAPIURL string
	pollDelay    time.Duration
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
	shapes, err := p.api.ListShapes(ctx)
	if err != nil {
		if transientAPIError(err) {
			return executionstore.MachineResourceFacts{}, fmt.Errorf(
				"list createos shapes: %w: %w",
				storeerr.ErrMachineProviderUnavailable,
				err,
			)
		}
		return executionstore.MachineResourceFacts{}, fmt.Errorf("list createos shapes: %w", err)
	}
	for _, candidate := range shapes {
		if candidate.ID != options.Shape {
			continue
		}
		if candidate.VCPU <= 0 || candidate.MemMiB <= 0 {
			return executionstore.MachineResourceFacts{}, fmt.Errorf(
				"createos shape %q has invalid resources",
				options.Shape,
			)
		}
		return executionstore.MachineResourceFacts{CPU: &candidate.VCPU, MemoryMB: &candidate.MemMiB}, nil
	}
	return executionstore.MachineResourceFacts{}, fmt.Errorf("createos shape %q was not found", options.Shape)
}

func (p *provider) ValidateMachineConfig(
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineEnv map[string]string,
) error {
	options, err := parseProviderOptions(machineProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	env, err := p.sandboxEnv(options, "", machineEnv)
	if err != nil {
		return err
	}
	return validateSandboxEnv(env)
}

func (p *provider) ProvisionMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
	_ bool,
) (providers.ProvisionMachineResult, error) {
	options, err := parseProviderOptions(machineProvisioning.ProviderOptions)
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
		env, err := p.sandboxEnv(options, machineToken, machineEnv)
		if err != nil {
			return providers.ProvisionMachineResult{}, err
		}
		if err := validateSandboxEnv(env); err != nil {
			return providers.ProvisionMachineResult{}, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
		}
		created, err := p.api.CreateSandbox(ctx, createSandboxRequest{
			Shape:  options.Shape,
			RootFS: options.RootFS,
			Name:   name,
			Envs:   env,
		})
		if err != nil {
			existing, existingFound, lookupErr := p.findByName(ctx, name)
			if lookupErr != nil || !existingFound {
				return providers.ProvisionMachineResult{}, err
			}
			target = existing
		} else {
			target, found, err = p.api.GetSandbox(ctx, created.ID)
			if err != nil {
				return providers.ProvisionMachineResult{ProviderResourceID: created.ID}, err
			}
			if !found {
				return providers.ProvisionMachineResult{}, fmt.Errorf(
					"created createos sandbox %q was not found",
					created.ID,
				)
			}
		}
	}
	if target.ID == "" || target.Name != name {
		return providers.ProvisionMachineResult{}, errors.New(
			"createos sandbox does not have the expected allocation name",
		)
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if options.SleepAfterMS > 0 {
		result.SandboxURL = apiBaseURL + "/v1/sandboxes/" + url.PathEscape(target.ID)
	}
	for target.Status == sandboxStatusCreating {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf(
				"wait for createos sandbox %q to start: %w",
				result.ProviderResourceID,
				ctx.Err(),
			)
		case <-time.After(p.pollDelay):
		}
		if target, found, err = p.api.GetSandbox(ctx, result.ProviderResourceID); err != nil {
			return result, err
		}
		if !found {
			return result, fmt.Errorf("createos sandbox %q disappeared while starting", result.ProviderResourceID)
		}
	}
	if target.Shape != options.Shape {
		return result, fmt.Errorf(
			"createos sandbox %q shape %q does not match %q",
			target.ID,
			target.Shape,
			options.Shape,
		)
	}
	switch target.Status {
	case sandboxStatusRunning:
	case sandboxStatusFailed, sandboxStatusDestroying, sandboxStatusDestroyed:
		return result, fmt.Errorf("createos sandbox %q is %s: %w", target.ID, target.Status, providers.ErrPermanent)
	default:
		return result, fmt.Errorf("createos sandbox %q is not running (status %q)", target.ID, target.Status)
	}
	if err := p.ensureDaemonProcess(ctx, target.ID); err != nil {
		return result, err
	}
	return result, nil
}

func (p *provider) ensureDaemonProcess(ctx context.Context, sandboxID string) error {
	processes, err := p.api.ListProcesses(ctx, sandboxID)
	if err != nil {
		return err
	}
	args := daemonLauncherArgs()
	if slices.ContainsFunc(processes, func(current process) bool {
		return current.Command == args[0] && slices.Equal(current.Args, args[1:]) && !current.LeaderExited &&
			(current.State == processStateStarting || current.State == processStateRunning)
	}) {
		return nil
	}
	created, err := p.api.CreateProcess(ctx, sandboxID, commandRequest{Command: args[0], Args: args[1:]})
	if err != nil {
		return err
	}
	if created.ID == "" {
		return errors.New("createos daemon process response is missing its process id")
	}
	return nil
}

func daemonLauncherArgs() []string {
	args := providers.ManagedDaemonLauncherArgs()
	args[2] = strings.TrimSpace(guestDevFixupScript) + "\n" + args[2]
	return args
}

func (p *provider) sandboxEnv(
	options providerOptions,
	machineToken string,
	machineEnv map[string]string,
) (map[string]string, error) {
	env, err := providers.BuildManagedMachineEnv(p.omnaraAPIURL, machineToken, options.StartupScript, machineEnv)
	if err != nil {
		return nil, err
	}
	maps.DeleteFunc(env, func(name, _ string) bool { return !sandboxEnvNamePattern.MatchString(name) })
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		env[daemonprotocol.WakeListenAddrEnvVar] = wakeListenAddr
		env[daemonprotocol.SleepPlatformEnvVar] = daemonprotocol.SleepPlatformCreateOS
	}
	return env, nil
}

func validateSandboxEnv(env map[string]string) error {
	if len(env) > maxSandboxEnvEntries {
		return fmt.Errorf(
			"createos env must have at most %d entries, including the ones Omnara adds",
			maxSandboxEnvEntries,
		)
	}
	size := 0
	for key, value := range env {
		if len(value) > maxSandboxEnvValueBytes {
			return fmt.Errorf("createos env %s must be at most %d bytes", key, maxSandboxEnvValueBytes)
		}
		size += len(key) + len(value) + sandboxEnvEntryOverhead
	}
	if size > maxSandboxEnvBytes {
		return fmt.Errorf("createos env must total at most %d bytes", maxSandboxEnvBytes)
	}
	return nil
}

func (p *provider) WakeMachine(ctx context.Context, input providers.WakeMachineInput) error {
	if input.ProviderResourceID == "" {
		return errors.New("provider resource id is required")
	}
	ctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	frozen := false
	settled := time.Now().Add(wakeSettlePolls * p.pollDelay)
	for {
		target, found, err := p.api.GetSandbox(ctx, input.ProviderResourceID)
		if err != nil && !transientAPIError(err) {
			return err
		}
		if err == nil && !found {
			return fmt.Errorf("createos sandbox %q was not found", input.ProviderResourceID)
		}
		switch target.Status {
		case "":
		case sandboxStatusPaused, sandboxStatusError:
			frozen = true
			err = p.api.ResumeSandbox(ctx, target.ID)
			if err != nil && apiStatusCode(err) != http.StatusConflict && !transientAPIError(err) {
				return err
			}
		case sandboxStatusPausing, sandboxStatusResuming:
			frozen = true
		case sandboxStatusRunning:
			if frozen || !time.Now().Before(settled) {
				if err = p.api.Exec(ctx, target.ID, wakePokeCommand); err == nil {
					return nil
				}
			}
		default:
			return fmt.Errorf("createos sandbox %q cannot wake from status %q", target.ID, target.Status)
		}
		delay := p.pollDelay
		if retryAfter, ok := providers.RetryAfter(err); ok {
			delay = max(delay, retryAfter)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wake createos sandbox %q: %w", input.ProviderResourceID, errors.Join(err, ctx.Err()))
		case <-time.After(delay):
		}
	}
}

func (p *provider) findByName(ctx context.Context, name string) (sandbox, bool, error) {
	for _, status := range liveSandboxStatuses {
		for offset := 0; ; {
			items, total, err := p.api.ListSandboxes(ctx, status, sandboxListPageSize, offset)
			if err != nil {
				return sandbox{}, false, err
			}
			for _, item := range items {
				if item.Name == name {
					return item, true, nil
				}
			}
			offset += len(items)
			if len(items) == 0 || offset >= total {
				break
			}
		}
	}
	return sandbox{}, false, nil
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	name, err := allocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	var target sandbox
	var found bool
	if providerResourceID == "" {
		target, found, err = p.findByName(ctx, name)
	} else {
		target, found, err = p.api.GetSandbox(ctx, providerResourceID)
	}
	if err != nil || !found || target.Status == sandboxStatusFailed || target.Status == sandboxStatusDestroyed {
		return "", false, err
	}
	if target.ID == "" || target.Name != name {
		return "", false, fmt.Errorf("createos sandbox %q does not have the expected allocation name", target.ID)
	}
	return target.ID, true, nil
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

func allocationName(installationID, machineID uuid.UUID) (string, error) {
	canonical, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return "omnara-" + hex.EncodeToString(sum[:])[:15], nil
}

var _ providers.MachineWaker = (*provider)(nil)
