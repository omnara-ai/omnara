package arker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout = time.Minute
	wakeSessionIdx      = 1
)

var (
	daemonLauncherCommand = providers.ManagedDaemonLauncherArgs()[2]
	envNamePattern        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

var errNotThisMachine = errors.New("arker vm does not belong to this machine")

var (
	_ providers.Provider        = (*provider)(nil)
	_ providers.RuntimeProvider = (*provider)(nil)
	_ providers.MachineWaker    = (*provider)(nil)
)

type provider struct {
	api          *restClient
	apiToken     string
	apiBaseURL   string
	omnaraAPIURL string
}

func (p *provider) regionBaseURL(region string) string {
	if p.apiBaseURL != "" {
		return p.apiBaseURL
	}
	return "https://" + region + ".arker.ai/api"
}

func (p *provider) apiFor(baseURL string) *restClient {
	if p.api != nil {
		return p.api
	}
	return &restClient{
		baseURL:    baseURL,
		apiToken:   p.apiToken,
		httpClient: providers.NewHTTPClient(),
	}
}

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
	maps.DeleteFunc(env, func(name, _ string) bool { return !envNamePattern.MatchString(name) })
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	command := daemonLauncherCommand
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		env[daemonprotocol.WakeListenAddrEnvVar] = ":" +
			strconv.Itoa(daemonprotocol.WakeListenerPort)
		env[daemonprotocol.SleepPlatformEnvVar] = daemonprotocol.SleepPlatformArker
		command = sleepBootCommand
	}
	baseURL := p.regionBaseURL(options.Region)
	api := p.apiFor(baseURL)
	target, err := api.Fork(ctx, forkRequest{
		SourceVMName: options.Source,
		Name:         name,
		Resources: resources{
			VCPU:      *machineProvisioning.CPU,
			MemoryMiB: *machineProvisioning.MemoryMB,
		},
	}, name)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if target.ID == "" {
		return providers.ProvisionMachineResult{}, errors.New("arker fork response is missing vm id")
	}
	if target.Name != name {
		return providers.ProvisionMachineResult{}, fmt.Errorf("%w: %s", errNotThisMachine, target.ID)
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if err := ensureDaemon(ctx, api, target.ID, command, env); err != nil {
		return result, err
	}
	if options.SleepAfterMS > 0 {
		result.SandboxURL = baseURL
	}
	return result, nil
}

func ensureDaemon(
	ctx context.Context,
	api *restClient,
	vmID string,
	command string,
	env map[string]string,
) error {
	runs, err := api.ListRuns(ctx, vmID)
	if err != nil {
		return err
	}
	for _, existing := range runs {
		if existing.Command == command && runLive(existing.State) {
			return nil
		}
	}
	sessionID, err := api.CreateSession(ctx, vmID, env)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return errors.New("arker session response is missing session id")
	}
	started, err := api.StartRun(ctx, vmID, runRequest{Command: command, SessionID: sessionID})
	if err != nil {
		return err
	}
	if started.State != "" && !runLive(started.State) {
		return fmt.Errorf("arker daemon run is %s instead of running", started.State)
	}
	return nil
}

func runLive(state string) bool {
	return state == "pending" || state == "running"
}

func (p *provider) WakeMachine(
	ctx context.Context,
	input providers.WakeMachineInput,
) error {
	if input.SandboxURL == "" {
		return errors.New("arker sandbox url is required")
	}
	sessionIdx := wakeSessionIdx
	started, err := p.apiFor(input.SandboxURL).StartRun(ctx, input.ProviderResourceID, runRequest{
		Command:    wakeCommand,
		SessionIdx: &sessionIdx,
	})
	if err != nil {
		return err
	}
	if started.State != "" && !runLive(started.State) {
		return fmt.Errorf("arker wake run is %s instead of running", started.State)
	}
	return nil
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	target, found, err := p.inspectVM(ctx, installationID, machineID, machineProvisioning, providerResourceID)
	return target.ID, found, err
}

func (p *provider) inspectVM(
	ctx context.Context,
	installationID uuid.UUID,
	machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (vm, bool, error) {
	region, err := existingMachineRegion(machineProvisioning)
	if err != nil {
		return vm{}, false, err
	}
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return vm{}, false, err
	}
	lookup := providerResourceID
	if lookup == "" {
		lookup = expectedName
	}
	target, found, err := p.apiFor(p.regionBaseURL(region)).GetVM(ctx, lookup)
	if err != nil || !found {
		return vm{}, false, err
	}
	if target.Name != expectedName {
		return vm{}, false, fmt.Errorf("%w: %s", errNotThisMachine, lookup)
	}
	if target.ID == "" {
		return vm{}, false, fmt.Errorf("arker vm %s is missing its id", lookup)
	}
	return target, true, nil
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
	region, err := existingMachineRegion(machineProvisioning)
	if err != nil {
		return err
	}
	return p.apiFor(p.regionBaseURL(region)).DeleteVM(ctx, resourceID)
}
