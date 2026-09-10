package modal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout = 2 * time.Minute
	sandboxTimeout      = 24 * time.Hour
	machineTag          = "omnara-machine"
	installationTag     = "omnara-installation"
)

type provider struct {
	api          apiClient
	app          string
	environment  string
	credential   providerCredential
	omnaraAPIURL string
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
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	api, err := p.apiClient()
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	if p.api == nil {
		defer api.Close()
	}
	if existing, found, err := api.GetSandboxByName(ctx, name); err != nil {
		return providers.ProvisionMachineResult{}, err
	} else if found {
		return provisionResult(existing, installationID, machineID)
	}
	installationOwner, machineOwner, err := sandboxOwnershipTagValues(installationID, machineID)
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
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	created, err := api.CreateSandbox(ctx, createSandboxRequest{
		Name:     name,
		Image:    options.Image,
		CPU:      float64(*machineProvisioning.CPU) / 2,
		MemoryMB: *machineProvisioning.MemoryMB,
		Timeout:  sandboxTimeout,
		Command:  providers.ManagedDaemonLauncherArgs(),
		Env:      env,
		Region:   options.Region,
		Tags:     map[string]string{installationTag: installationOwner, machineTag: machineOwner},
	})
	if err != nil {
		if existing, found, inspectErr := api.GetSandboxByName(ctx, name); inspectErr == nil && found {
			return provisionResult(existing, installationID, machineID)
		}
		return providers.ProvisionMachineResult{}, err
	}
	return provisionResult(created, installationID, machineID)
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	api, err := p.apiClient()
	if err != nil {
		return "", false, err
	}
	if p.api == nil {
		defer api.Close()
	}
	return inspectMachine(ctx, api, installationID, machineID, providerResourceID)
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID storage.ID,
	machineID storage.ID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	if providerResourceID == "" {
		return errors.New("provider resource id is required")
	}
	api, err := p.apiClient()
	if err != nil {
		return err
	}
	if p.api == nil {
		defer api.Close()
	}
	resourceID, found, err := inspectMachine(ctx, api, installationID, machineID, providerResourceID)
	if err == nil && !found {
		resourceID, found, err = inspectMachine(ctx, api, installationID, machineID, "")
	}
	if err != nil || !found {
		return err
	}
	return api.DeleteSandbox(ctx, resourceID)
}

func (p *provider) apiClient() (apiClient, error) {
	if p.api != nil {
		return p.api, nil
	}
	return newModalAPI(p.app, p.environment, p.credential)
}

func inspectMachine(
	ctx context.Context,
	api apiClient,
	installationID storage.ID,
	machineID storage.ID,
	providerResourceID string,
) (string, bool, error) {
	expectedName, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	var target sandbox
	var found bool
	if providerResourceID != "" {
		target, found, err = api.GetSandboxByID(ctx, providerResourceID)
	} else {
		target, found, err = api.GetSandboxByName(ctx, expectedName)
	}
	if err != nil || !found {
		return "", false, err
	}
	if !target.Running {
		return "", false, nil
	}
	result, err := provisionResult(target, installationID, machineID)
	if err != nil {
		return "", false, err
	}
	return result.ProviderResourceID, true, nil
}

func provisionResult(target sandbox, installationID, machineID storage.ID) (providers.ProvisionMachineResult, error) {
	result := providers.ProvisionMachineResult{ProviderResourceID: target.ID}
	if target.ID == "" {
		return result, errors.New("modal sandbox is missing its id")
	}
	if !sandboxOwnedBy(target, installationID, machineID) {
		return providers.ProvisionMachineResult{}, fmt.Errorf(
			"modal sandbox %q does not have the expected ownership tags", target.ID,
		)
	}
	if !target.Running {
		return providers.ProvisionMachineResult{}, fmt.Errorf(
			"modal sandbox %q has terminated; provisioning must be retried: %w",
			target.ID,
			providers.ErrResourceReplaced,
		)
	}
	return result, nil
}

func sandboxOwnedBy(target sandbox, installationID, machineID storage.ID) bool {
	installationOwner, machineOwner, err := sandboxOwnershipTagValues(installationID, machineID)
	if err != nil {
		return false
	}
	return target.Tags[installationTag] == installationOwner && target.Tags[machineTag] == machineOwner
}

func sandboxOwnershipTagValues(installationID, machineID storage.ID) (string, string, error) {
	installationOwner, err := publicid.Encode(publicid.KindInstallation, installationID)
	if err != nil {
		return "", "", err
	}
	machineOwner, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		return "", "", err
	}
	return installationOwner, machineOwner, nil
}
