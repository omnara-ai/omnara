package unikraft

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout   = 5 * time.Second
	scaleToZeroCooldownMS = 2_000
	wakeRequestTimeout    = 2 * time.Second
)

type provider struct {
	api           apiClient
	apiToken      string
	apiBaseURL    string
	omnaraAPIURL  string
	wakeTransport http.RoundTripper
}

func (p *provider) apiForMetro(metro string) apiClient {
	if p.api != nil {
		return p.api
	}
	baseURL := p.apiBaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://api.%s.unikraft.cloud", metro)
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
	api := p.apiForMetro(options.Metro)
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if existing, found, err := api.GetInstanceByName(ctx, name); err != nil {
		return providers.ProvisionMachineResult{}, err
	} else if found {
		if existing.UUID == "" {
			return providers.ProvisionMachineResult{}, errors.New(
				"unikraft instance lookup is missing instance uuid",
			)
		}
		return p.provisionResult(ctx, api, options, existing.UUID)
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
	if options.SleepAfterMS > 0 {
		env[daemonprotocol.SleepAfterEnvVar] = strconv.Itoa(options.SleepAfterMS)
		env[daemonprotocol.WakeListenAddrEnvVar] = ":" +
			strconv.Itoa(daemonprotocol.WakeListenerPort)
		env[daemonprotocol.SleepPlatformEnvVar] = daemonprotocol.SleepPlatformUnikraft
	}
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	args := providers.ManagedDaemonLauncherArgs()
	if options.SleepAfterMS > 0 {
		args[2] = unikraftScaleToZeroBootGuardScript() + args[2]
	}
	create := createInstanceRequest{
		Name:          name,
		Image:         options.Image,
		Args:          args,
		Env:           env,
		MemoryMB:      *machineProvisioning.MemoryMB,
		VCPUs:         *machineProvisioning.CPU,
		Autostart:     true,
		RestartPolicy: "never",
	}
	if options.SleepAfterMS > 0 {
		create.ScaleToZero = &scaleToZero{
			Policy:         "on",
			Stateful:       true,
			CooldownTimeMS: scaleToZeroCooldownMS,
		}
		create.ServiceGroup = &serviceGroup{
			Services: []service{{
				Port:            443,
				DestinationPort: daemonprotocol.WakeListenerPort,
				Handlers:        []string{"http", "tls"},
			}},
		}
	}
	created, err := api.CreateInstance(ctx, create)
	if err != nil {
		if existing, found, inspectErr := api.GetInstanceByName(ctx, name); inspectErr == nil && found {
			if existing.UUID == "" {
				return providers.ProvisionMachineResult{}, errors.New(
					"unikraft instance lookup is missing instance uuid",
				)
			}
			return p.provisionResult(ctx, api, options, existing.UUID)
		}
		return providers.ProvisionMachineResult{}, err
	}
	if created.UUID == "" {
		return providers.ProvisionMachineResult{}, errors.New(
			"unikraft create response is missing instance uuid",
		)
	}
	return p.provisionResult(ctx, api, options, created.UUID)
}

func unikraftScaleToZeroBootGuardScript() string {
	return `printf${IFS}'=1'${IFS}>` + daemonprotocol.UnikraftScaleToZeroControlFilePath +
		`||(echo${IFS}` +
		`omnara${IFS}boot${IFS}could${IFS}not${IFS}disable${IFS}unikraft${IFS}scale-to-zero>&2;` +
		`exit${IFS}1)||exit${IFS}1;`
}

func (p *provider) provisionResult(
	ctx context.Context,
	api apiClient,
	options providerOptions,
	uuid string,
) (providers.ProvisionMachineResult, error) {
	result := providers.ProvisionMachineResult{ProviderResourceID: uuid}
	if options.SleepAfterMS <= 0 {
		return result, nil
	}
	instance, found, err := api.GetInstanceByUUID(ctx, uuid)
	if err != nil {
		return result, err
	}
	if !found {
		return result, errors.New(
			"unikraft instance disappeared while resolving sandbox url",
		)
	}
	fqdn := instance.wakeFQDN()
	if fqdn == "" {
		return result, errors.New(
			"unikraft instance is missing a service group fqdn for its sandbox url",
		)
	}
	result.SandboxURL = "https://" + fqdn + "/"
	return result, nil
}

func (p *provider) WakeMachine(
	ctx context.Context,
	input providers.WakeMachineInput,
) error {
	if input.SandboxURL == "" {
		return errors.New("unikraft sandbox url is required")
	}
	wakeCtx, cancel := context.WithTimeout(ctx, wakeRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(wakeCtx, http.MethodGet, input.SandboxURL, nil)
	if err != nil {
		return fmt.Errorf("build unikraft wake request: %w", err)
	}
	client := providers.NewHTTPClient()
	if p.wakeTransport != nil {
		client.Transport = p.wakeTransport
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("wake unikraft machine: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("wake unikraft machine: unexpected HTTP status %d", response.StatusCode)
	}
	return nil
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	metro, valid := existingMachineMetro(machineProvisioning)
	if !valid {
		return "", false, errors.New("unikraft stored machine config requires a valid metro")
	}
	api := p.apiForMetro(metro)
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	var result instance
	var found bool
	if providerResourceID != "" {
		result, found, err = api.GetInstanceByUUID(ctx, providerResourceID)
	} else {
		result, found, err = api.GetInstanceByName(ctx, expectedName)
	}
	if err != nil || !found {
		return "", false, err
	}
	if result.UUID == "" {
		return "", false, errors.New("unikraft instance lookup is missing instance uuid")
	}
	if result.Name != expectedName {
		return "", false, fmt.Errorf("unikraft instance %q does not have the expected allocation name", result.UUID)
	}
	return result.UUID, true, nil
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
	metro, valid := existingMachineMetro(machineProvisioning)
	if !valid {
		return errors.New("unikraft stored machine config requires a valid metro")
	}
	return p.apiForMetro(metro).DeleteInstanceByUUID(ctx, resourceID)
}
