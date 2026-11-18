package docker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	provisioningTimeout = 2 * time.Minute
	installationLabel   = "omnara-installation"
	machineLabel        = "omnara-machine"
	mebibyte            = 1024 * 1024
	nanoCPUsPerCPU      = 1_000_000_000
	processLimit        = 16384
	logDriver           = "local"
)

var errNotThisMachine = errors.New("docker resource does not belong to this machine")

var (
	_ providers.Provider        = (*provider)(nil)
	_ providers.RuntimeProvider = (*provider)(nil)
)

type provider struct {
	api          apiClient
	runtime      string
	omnaraAPIURL string
}

type machineLimits struct {
	nanoCPUs   int64
	memory     int64
	pidsLimit  int64
	memorySwap int64
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
	installationID, machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineToken string,
	machineEnv map[string]string,
	_ bool,
) (providers.ProvisionMachineResult, error) {
	options, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	name, labels, err := machineIdentity(installationID, machineID)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	env, err := providers.BuildManagedMachineEnv(p.omnaraAPIURL, machineToken, options.StartupScript, machineEnv)
	if err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	env[providers.ManagedBootstrapScriptEnvVar] = providers.ManagedBootScriptPayload()
	if err := p.ensureNetwork(ctx, name, labels); err != nil {
		return providers.ProvisionMachineResult{}, err
	}
	result := providers.ProvisionMachineResult{ProviderResourceID: name}
	limits := limitsFor(machineProvisioning)
	request := p.containerRequest(name, labels, options.Image, env, limits)
	created, err := p.api.CreateContainer(ctx, name, request)
	if isNotFound(err) {
		if err := p.api.PullImage(ctx, options.Image); err != nil {
			if isNotFound(err) {
				err = fmt.Errorf("%w: %w", err, providers.ErrPermanent)
			}
			return result, fmt.Errorf(
				"pull docker image %q; for a private image, pull it on the Docker host first: %w",
				options.Image,
				err,
			)
		}
		created, err = p.api.CreateContainer(ctx, name, request)
	}
	switch {
	case err == nil:
		if len(created.Warnings) > 0 {
			return result, fmt.Errorf(
				"docker could not apply the machine settings: %s: %w",
				strings.Join(created.Warnings, "; "),
				providers.ErrPermanent,
			)
		}
	case isConflict(err):
		return result, p.resumeExistingContainer(ctx, name, labels, limits)
	case isClientError(err):
		return result, fmt.Errorf("%w: %w", err, providers.ErrPermanent)
	default:
		return result, err
	}
	return result, p.api.StartContainer(ctx, name)
}

func (p *provider) ensureNetwork(ctx context.Context, name string, labels map[string]string) error {
	err := p.api.CreateNetwork(ctx, networkCreateRequest{Name: name, Driver: "bridge", Labels: labels})
	if err == nil {
		return nil
	}
	if !isConflict(err) {
		return fmt.Errorf("create docker network %q: %w", name, err)
	}
	existing, found, inspectErr := p.api.InspectNetwork(ctx, name)
	if inspectErr != nil {
		return fmt.Errorf("inspect docker network %q: %w", name, inspectErr)
	}
	if !found {
		return fmt.Errorf("docker network %q conflicted but was not found", name)
	}
	if !ownedBy(existing.Labels, labels) {
		return fmt.Errorf("%w: network %s", errNotThisMachine, name)
	}
	return nil
}

func (p *provider) resumeExistingContainer(
	ctx context.Context,
	name string,
	labels map[string]string,
	limits machineLimits,
) error {
	existing, found, err := p.api.InspectContainer(ctx, name)
	if err != nil {
		return fmt.Errorf("inspect docker container %q: %w", name, err)
	}
	if !found {
		return fmt.Errorf("docker container %q conflicted but was not found", name)
	}
	if !ownedBy(existing.Config.Labels, labels) {
		return fmt.Errorf("%w: container %s", errNotThisMachine, name)
	}
	if got := limitsOf(existing.HostConfig); got != limits {
		return fmt.Errorf(
			"docker container %q does not have the machine's resource limits: %w",
			name,
			providers.ErrPermanent,
		)
	}
	switch existing.State.Status {
	case "created":
		return p.api.StartContainer(ctx, name)
	case "running":
		return nil
	case "exited", "dead":
		if err := p.api.RemoveContainer(ctx, name); err != nil {
			return err
		}
		return fmt.Errorf(
			"docker container %q stopped and was removed; provisioning must be retried: %w",
			name,
			providers.ErrResourceReplaced,
		)
	default:
		return fmt.Errorf("docker container %q is %s", name, existing.State.Status)
	}
}

func (p *provider) containerRequest(
	name string,
	labels map[string]string,
	image string,
	env map[string]string,
	limits machineLimits,
) containerCreateRequest {
	containerEnv := make([]string, 0, len(env))
	for _, key := range slices.Sorted(maps.Keys(env)) {
		containerEnv = append(containerEnv, key+"="+env[key])
	}
	return containerCreateRequest{
		Image:      image,
		Entrypoint: providers.ManagedDaemonLauncherArgs(),
		Env:        containerEnv,
		Labels:     labels,
		HostConfig: hostConfig{
			NanoCPUs:    limits.nanoCPUs,
			Memory:      limits.memory,
			MemorySwap:  limits.memorySwap,
			PidsLimit:   limits.pidsLimit,
			Init:        true,
			CapDrop:     []string{"NET_RAW"},
			LogConfig:   logConfig{Type: logDriver},
			Runtime:     p.runtime,
			NetworkMode: name,
		},
	}
}

func limitsFor(machineProvisioning executionstore.MachineProvisioningConfig) machineLimits {
	memory := int64(*machineProvisioning.MemoryMB) * mebibyte
	return machineLimits{
		nanoCPUs:   int64(*machineProvisioning.CPU) * nanoCPUsPerCPU,
		memory:     memory,
		memorySwap: memory,
		pidsLimit:  processLimit,
	}
}

func limitsOf(config hostConfig) machineLimits {
	return machineLimits{
		nanoCPUs:   config.NanoCPUs,
		memory:     config.Memory,
		memorySwap: config.MemorySwap,
		pidsLimit:  config.PidsLimit,
	}
}

func (p *provider) InspectMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) (string, bool, error) {
	name, labels, err := machineIdentity(installationID, machineID)
	if err != nil {
		return "", false, err
	}
	if providerResourceID != "" && providerResourceID != name {
		return "", false, fmt.Errorf("%w: %s", errNotThisMachine, providerResourceID)
	}
	existing, found, err := p.api.InspectContainer(ctx, name)
	if err != nil {
		return "", false, err
	}
	if found {
		if !ownedBy(existing.Config.Labels, labels) {
			return "", false, fmt.Errorf("%w: container %s", errNotThisMachine, name)
		}
		return name, true, nil
	}
	existingNetwork, found, err := p.api.InspectNetwork(ctx, name)
	if err != nil || !found {
		return "", false, err
	}
	if !ownedBy(existingNetwork.Labels, labels) {
		return "", false, fmt.Errorf("%w: network %s", errNotThisMachine, name)
	}
	return name, true, nil
}

func (p *provider) CheckConnection(ctx context.Context, _ executionstore.MachineProvisioningConfig) error {
	return p.api.Ping(ctx)
}

func (p *provider) DeleteMachine(
	ctx context.Context,
	installationID, machineID uuid.UUID,
	_ executionstore.MachineProvisioningConfig,
	providerResourceID string,
) error {
	if providerResourceID == "" {
		return errors.New("provider resource id is required")
	}
	name, labels, err := machineIdentity(installationID, machineID)
	if err != nil {
		return err
	}
	if providerResourceID != name {
		return fmt.Errorf("%w: %s", errNotThisMachine, providerResourceID)
	}
	existing, found, err := p.api.InspectContainer(ctx, name)
	if err != nil {
		return err
	}
	if found {
		if !ownedBy(existing.Config.Labels, labels) {
			return fmt.Errorf("%w: container %s", errNotThisMachine, name)
		}
		if err := p.api.RemoveContainer(ctx, name); err != nil {
			return err
		}
	}
	existingNetwork, found, err := p.api.InspectNetwork(ctx, name)
	if err != nil || !found {
		return err
	}
	if !ownedBy(existingNetwork.Labels, labels) {
		return fmt.Errorf("%w: network %s", errNotThisMachine, name)
	}
	return p.api.RemoveNetwork(ctx, name)
}

func machineIdentity(installationID, machineID uuid.UUID) (string, map[string]string, error) {
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		return "", nil, err
	}
	installationOwner, err := publicid.Encode(publicid.KindInstallation, installationID)
	if err != nil {
		return "", nil, err
	}
	machineOwner, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		return "", nil, err
	}
	return name, map[string]string{installationLabel: installationOwner, machineLabel: machineOwner}, nil
}

func ownedBy(actual, expected map[string]string) bool {
	return actual[installationLabel] == expected[installationLabel] &&
		actual[machineLabel] == expected[machineLabel]
}
