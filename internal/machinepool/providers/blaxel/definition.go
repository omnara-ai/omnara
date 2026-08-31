package blaxel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type providerConfig struct {
	Workspace      string   `json:"workspace"`
	AllowedImages  []string `json:"allowed_images,omitempty"`
	AllowedRegions []string `json:"allowed_regions,omitempty"`
}

type providerOptions struct {
	Image         string `json:"image"`
	Region        string `json:"region"`
	StartupScript string `json:"startup_script"`
	SleepAfterMS  int    `json:"sleep_after_ms"`
}

type Definition struct{}

var _ providers.RuntimeProviderDefinition = Definition{}

func resourcePolicy() providers.MachineResourcePolicy {
	return providers.MachineResourcePolicy{
		CPU: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceUnsupported,
			Limits:       providers.MachineResourceUnsupported,
			Provisioning: providers.MachineResourceUnmodeled,
		},
		MemoryMB: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceRequired,
			Limits:       providers.MachineResourceRequired,
			Provisioning: providers.MachineResourceConfigured,
		},
	}
}

func (Definition) NewProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (providers.Provider, error) {
	return newProvider(raw, runtimeConfig)
}

func (Definition) NewRuntimeProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (providers.RuntimeProvider, error) {
	return newProvider(raw, runtimeConfig)
}

func newProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (*provider, error) {
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(runtimeConfig.ProviderAuthToken) == "" {
		return nil, errors.New("blaxel provider auth token is required")
	}
	return &provider{
		workspace: config.Workspace,
		apiToken:  runtimeConfig.ProviderAuthToken,
		omnara:    runtimeConfig.Omnara,
	}, nil
}

func (Definition) ResolveMachineProviderOptions(
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	return providers.MergeOptions(defaultOptions, projectOptions, agentOptions)
}

func (Definition) ValidatePool(
	policy executionstore.MachinePoolProviderPolicy,
) error {
	if err := providers.ValidateMachinePoolResourcePolicy(providers.Blaxel, policy, resourcePolicy()); err != nil {
		return err
	}
	defaultOptions, err := providerOptionsFromProvisioning(policy.DefaultProvisioning)
	if err != nil {
		return err
	}
	parsedProviderConfig, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	if err := providers.ValidateAllowedValue(
		"blaxel image",
		"allowed_images",
		defaultOptions.Image,
		parsedProviderConfig.AllowedImages,
		defaultOptions.Image,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"blaxel region",
		"allowed_regions",
		defaultOptions.Region,
		parsedProviderConfig.AllowedRegions,
		defaultOptions.Region,
	)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Blaxel,
		machineProvisioning,
		resourcePolicy(),
	); err != nil {
		return err
	}
	defaultOptions, err := providerOptionsFromProvisioning(policy.DefaultProvisioning)
	if err != nil {
		return err
	}
	machineOptions, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
		return err
	}
	parsedProviderConfig, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	if err := providers.ValidateAllowedValue(
		"blaxel image",
		"allowed_images",
		machineOptions.Image,
		parsedProviderConfig.AllowedImages,
		defaultOptions.Image,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"blaxel region",
		"allowed_regions",
		machineOptions.Region,
		parsedProviderConfig.AllowedRegions,
		defaultOptions.Region,
	)
}

func (definition Definition) BuildMachineProvisioningIntent(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := definition.ValidateMachineProvisioning(policy, machineProvisioning); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode blaxel provider config: %w", err)
	}
	config.Workspace = strings.TrimSpace(config.Workspace)
	if err := providers.ValidateDNSLabel(config.Workspace); err != nil {
		return providerConfig{}, fmt.Errorf("blaxel workspace: %w", err)
	}
	var err error
	config.AllowedImages, err = providers.NormalizeAllowlist(
		"blaxel provider config allowed_images",
		config.AllowedImages,
		providers.ValidateImageRef,
	)
	if err != nil {
		return providerConfig{}, err
	}
	config.AllowedRegions, err = providers.NormalizeAllowlist(
		"blaxel provider config allowed_regions",
		config.AllowedRegions,
		providers.ValidateDNSLabel,
	)
	if err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func providerOptionsFromProvisioning(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if machineProvisioning.CPU != nil {
		return providerOptions{}, errors.New("blaxel machine config does not support cpu")
	}
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("blaxel machine config requires positive memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(
	rawOptions map[string]json.RawMessage,
) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("blaxel machine config requires provider_options")
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode blaxel provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode blaxel provider_options: %w", err)
	}
	options.Image = strings.TrimSpace(options.Image)
	if err := providers.ValidateImageRef(options.Image); err != nil {
		return providerOptions{}, fmt.Errorf("blaxel machine config image: %w", err)
	}
	if options.Region == "" {
		return providerOptions{}, errors.New("blaxel machine config requires region")
	}
	if err := providers.ValidateDNSLabel(options.Region); err != nil {
		return providerOptions{}, fmt.Errorf("blaxel machine config region: %w", err)
	}
	if err := providers.ValidateManagedStartupScript("blaxel machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	if options.SleepAfterMS != 0 {
		if _, err := daemonprotocol.SleepAfterDuration(options.SleepAfterMS); err != nil {
			return providerOptions{}, fmt.Errorf("blaxel machine config sleep_after_ms %w", err)
		}
	}
	return options, nil
}
