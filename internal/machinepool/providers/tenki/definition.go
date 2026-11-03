package tenki

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type providerConfig struct {
	AllowedImages []string `json:"allowed_images,omitempty"`
}

type providerOptions struct {
	Image         string `json:"image,omitempty"`
	DiskSizeGB    int32  `json:"disk_size_gb,omitempty"`
	StartupScript string `json:"startup_script,omitempty"`
}

type Definition struct{}

var _ providers.Definition = Definition{}
var _ providers.RuntimeProviderDefinition = Definition{}

func (Definition) ResourcePolicy() providers.MachineResourcePolicy {
	return providers.MachineResourcePolicy{
		CPU: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceRequired,
			Limits:       providers.MachineResourceRequired,
			Provisioning: providers.MachineResourceConfigured,
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
	config providers.RuntimeConfig,
) (providers.Provider, error) {
	return newProvider(raw, config)
}

func (Definition) NewRuntimeProvider(
	raw json.RawMessage,
	config providers.RuntimeConfig,
) (providers.RuntimeProvider, error) {
	return newProvider(raw, config)
}

func newProvider(raw json.RawMessage, runtimeConfig providers.RuntimeConfig) (*provider, error) {
	if _, err := parseProviderConfig(raw); err != nil {
		return nil, err
	}
	if strings.TrimSpace(runtimeConfig.ProviderAuthToken) == "" {
		return nil, errors.New("tenki provider auth token is required")
	}
	return &provider{
		api:          newRESTClient(apiBaseURL, runtimeConfig.ProviderAuthToken, providers.NewHTTPClient()),
		omnaraAPIURL: runtimeConfig.OmnaraAPIURL,
	}, nil
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return config, fmt.Errorf("decode tenki provider config: %w", err)
	}
	var err error
	config.AllowedImages, err = providers.NormalizeAllowlist(
		"tenki provider config allowed_images",
		config.AllowedImages,
		providers.ValidateImageRef,
	)
	return config, err
}

func (Definition) ResolveMachineProviderOptions(
	defaults, project, agent map[string]json.RawMessage,
) map[string]json.RawMessage {
	return provideroptions.Merge(defaults, project, agent)
}

func (d Definition) ValidatePool(policy executionstore.MachinePoolProviderPolicy) error {
	if err := providers.ValidateMachinePoolResourcePolicy(
		providers.Tenki,
		policy,
		d.ResourcePolicy(),
	); err != nil {
		return err
	}
	options, err := providerOptionsFromProvisioning(policy.DefaultProvisioning)
	if err != nil {
		return err
	}
	config, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil || options.Image == "" {
		return err
	}
	return providers.ValidateAllowedValue(
		"tenki image",
		"allowed_images",
		options.Image,
		config.AllowedImages,
		options.Image,
	)
}

func (d Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	config executionstore.MachineProvisioningConfig,
) error {
	if err := d.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Tenki,
		config,
		d.ResourcePolicy(),
	); err != nil {
		return err
	}
	options, err := providerOptionsFromProvisioning(config)
	if err != nil {
		return err
	}
	defaults, err := parseProviderOptions(policy.DefaultProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	parsedProviderConfig, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil || options.Image == "" {
		return err
	}
	return providers.ValidateAllowedValue(
		"tenki image",
		"allowed_images",
		options.Image,
		parsedProviderConfig.AllowedImages,
		defaults.Image,
	)
}

func (d Definition) BuildMachineProvisioningIntent(
	policy executionstore.MachinePoolProviderPolicy,
	config executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := d.ValidateMachineProvisioning(policy, config); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return config, nil
}

func providerOptionsFromProvisioning(
	config executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if config.CPU == nil || *config.CPU <= 0 {
		return providerOptions{}, errors.New("tenki machine config requires positive cpu")
	}
	if config.MemoryMB == nil || *config.MemoryMB <= 0 {
		return providerOptions{}, errors.New("tenki machine config requires positive memory_mb")
	}
	if *config.MemoryMB%2 != 0 {
		return providerOptions{}, errors.New("tenki machine config memory_mb must be a multiple of 2")
	}
	return parseProviderOptions(config.ProviderOptions)
}

func parseProviderOptions(raw map[string]json.RawMessage) (providerOptions, error) {
	var options providerOptions
	if raw == nil {
		return options, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return options, fmt.Errorf("encode tenki provider_options: %w", err)
	}
	if err := providers.DecodeStrictJSON(encoded, &options); err != nil {
		return options, fmt.Errorf("decode tenki provider_options: %w", err)
	}
	options.Image = strings.TrimSpace(options.Image)
	if _, ok := raw["image"]; ok && options.Image == "" {
		return options, errors.New("tenki machine config image must be non-empty; omit it to use the base image")
	}
	if options.Image != "" {
		if err := providers.ValidateImageRef(options.Image); err != nil {
			return options, fmt.Errorf("tenki machine config image: %w", err)
		}
	}
	if options.DiskSizeGB != 0 && (options.DiskSizeGB < 5 || options.DiskSizeGB > 100) {
		return options, errors.New("tenki machine config disk_size_gb must be between 5 and 100")
	}
	return options, providers.ValidateManagedStartupScript("tenki machine config", options.StartupScript)
}

func (o providerOptions) diskSizeGB() int32 {
	if o.DiskSizeGB == 0 && o.Image == "" {
		return baseImageDiskSizeGB
	}
	return o.DiskSizeGB
}
