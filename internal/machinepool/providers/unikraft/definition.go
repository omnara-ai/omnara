package unikraft

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type providerConfig struct {
	APIBaseURL    string   `json:"api_base_url,omitempty"`
	AllowedImages []string `json:"allowed_images,omitempty"`
	AllowedMetros []string `json:"allowed_metros,omitempty"`
}

type Definition struct{}

func (Definition) SupportsRuntimeObservation() bool { return true }

func resourcePolicy() providers.MachineResourcePolicy {
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
	runtimeConfig providers.RuntimeConfig,
) (providers.Provider, error) {
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(runtimeConfig.ProviderAuthToken) == "" {
		return nil, errors.New("unikraft provider auth token is required")
	}
	return &provider{
		apiToken:        runtimeConfig.ProviderAuthToken,
		apiBaseURL:      config.APIBaseURL,
		omnaraPublicURL: strings.TrimRight(strings.TrimSpace(runtimeConfig.PublicURL), "/"),
	}, nil
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode unikraft provider config: %w", err)
	}
	config.APIBaseURL = strings.TrimSpace(config.APIBaseURL)
	if config.APIBaseURL != "" {
		normalizedBaseURL, err := normalizeAPIBaseURL(config.APIBaseURL)
		if err != nil {
			return providerConfig{}, err
		}
		config.APIBaseURL = normalizedBaseURL
	}
	var err error
	config.AllowedImages, err = providers.NormalizeAllowlist(
		"unikraft provider config allowed_images",
		config.AllowedImages,
		providers.ValidateImageRef,
	)
	if err != nil {
		return providerConfig{}, err
	}
	config.AllowedMetros, err = providers.NormalizeAllowlist(
		"unikraft provider config allowed_metros",
		config.AllowedMetros,
		providers.ValidateDNSLabel,
	)
	if err != nil {
		return providerConfig{}, err
	}
	return config, nil
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
	if err := providers.ValidateMachinePoolResourcePolicy(providers.Unikraft, policy, resourcePolicy()); err != nil {
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
		"unikraft image",
		"allowed_images",
		defaultOptions.Image,
		parsedProviderConfig.AllowedImages,
		defaultOptions.Image,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"unikraft metro",
		"allowed_metros",
		defaultOptions.Metro,
		parsedProviderConfig.AllowedMetros,
		defaultOptions.Metro,
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
		providers.Unikraft,
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
		"unikraft image",
		"allowed_images",
		machineOptions.Image,
		parsedProviderConfig.AllowedImages,
		defaultOptions.Image,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"unikraft metro",
		"allowed_metros",
		machineOptions.Metro,
		parsedProviderConfig.AllowedMetros,
		defaultOptions.Metro,
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

type providerOptions struct {
	Image         string `json:"image"`
	Metro         string `json:"metro"`
	StartupScript string `json:"startup_script"`
	SleepAfterMS  int    `json:"sleep_after_ms"`
}

func providerOptionsFromProvisioning(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("unikraft machine config requires positive memory_mb")
	}
	if machineProvisioning.CPU == nil || *machineProvisioning.CPU <= 0 {
		return providerOptions{}, errors.New("unikraft machine config requires positive cpu")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("unikraft machine config requires provider_options")
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode unikraft provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode unikraft provider_options: %w", err)
	}
	options.Image = strings.TrimSpace(options.Image)
	if err := providers.ValidateImageRef(options.Image); err != nil {
		return providerOptions{}, fmt.Errorf("unikraft machine config image: %w", err)
	}
	if options.Metro == "" {
		return providerOptions{}, errors.New("unikraft machine config requires metro")
	}
	if err := providers.ValidateDNSLabel(options.Metro); err != nil {
		return providerOptions{}, fmt.Errorf("unikraft machine config metro: %w", err)
	}
	if err := providers.ValidateManagedStartupScript(
		"unikraft machine config",
		options.StartupScript,
	); err != nil {
		return providerOptions{}, err
	}
	if options.SleepAfterMS != 0 {
		if _, err := daemonprotocol.SleepAfterDuration(options.SleepAfterMS); err != nil {
			return providerOptions{}, fmt.Errorf("unikraft machine config sleep_after_ms %w", err)
		}
	}
	return options, nil
}

func normalizeAPIBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("unikraft api base url must be an absolute URL")
	}
	if !providers.IsHTTPS(parsed) {
		return "", errors.New("unikraft api base url must use https")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("unikraft api base url must not include path, query, or fragment")
	}
	return parsed.String(), nil
}
