package tenki

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const defaultAPIBaseURL = "https://api.tenki.cloud"

type providerConfig struct {
	APIBaseURL    string   `json:"api_base_url,omitempty"`
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

func newProvider(raw json.RawMessage, runtime providers.RuntimeConfig) (*provider, error) {
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(runtime.ProviderAuthToken) == "" {
		return nil, errors.New("tenki provider auth token is required")
	}
	return &provider{
		api:          &sdkClient{baseURL: config.APIBaseURL, token: runtime.ProviderAuthToken},
		omnaraAPIURL: runtime.OmnaraAPIURL,
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
	config.APIBaseURL = strings.TrimRight(strings.TrimSpace(config.APIBaseURL), "/")
	if config.APIBaseURL == "" {
		config.APIBaseURL = defaultAPIBaseURL
	}
	u, err := url.Parse(config.APIBaseURL)
	if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" {
		return config, errors.New(
			"tenki api_base_url must be an absolute HTTPS URL without credentials, query, or fragment",
		)
	}
	config.AllowedImages, err = providers.NormalizeAllowlist(
		"tenki allowed_images",
		config.AllowedImages,
		func(image string) error {
			if image == "" {
				return nil
			}
			return providers.ValidateImageRef(image)
		},
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
	if err != nil {
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
	providerConfig, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"tenki image",
		"allowed_images",
		options.Image,
		providerConfig.AllowedImages,
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
	if config.CPU == nil || *config.CPU < 1 || *config.CPU > 16 {
		return providerOptions{}, errors.New("tenki cpu must be between 1 and 16")
	}
	if config.MemoryMB == nil || *config.MemoryMB < 512 || *config.MemoryMB > 65536 {
		return providerOptions{}, errors.New("tenki memory_mb must be between 512 and 65536")
	}
	if *config.MemoryMB%2 != 0 {
		return providerOptions{}, errors.New("tenki memory_mb must be aligned to 2 MiB")
	}
	return parseProviderOptions(config.ProviderOptions)
}

func parseProviderOptions(raw map[string]json.RawMessage) (providerOptions, error) {
	options := providerOptions{DiskSizeGB: 20}
	if raw == nil {
		return options, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return options, err
	}
	if err := providers.DecodeStrictJSON(encoded, &options); err != nil {
		return options, fmt.Errorf("decode tenki provider_options: %w", err)
	}
	options.Image = strings.TrimSpace(options.Image)
	if options.Image != "" {
		if err := providers.ValidateImageRef(options.Image); err != nil {
			return options, fmt.Errorf("tenki image: %w", err)
		}
	}
	if options.DiskSizeGB < 5 || options.DiskSizeGB > 100 {
		return options, errors.New("tenki disk_size_gb must be between 5 and 100")
	}
	return options, providers.ValidateManagedStartupScript("tenki machine config", options.StartupScript)
}
