package arker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type providerConfig struct {
	APIBaseURL     string   `json:"api_base_url,omitempty"`
	AllowedSources []string `json:"allowed_sources,omitempty"`
	AllowedRegions []string `json:"allowed_regions,omitempty"`
}

type providerOptions struct {
	Source        string `json:"source"`
	Region        string `json:"region"`
	StartupScript string `json:"startup_script"`
	SleepAfterMS  int    `json:"sleep_after_ms"`
}

type Definition struct{}

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
		return nil, errors.New("arker provider auth token is required")
	}
	return &provider{
		apiToken:     runtimeConfig.ProviderAuthToken,
		apiBaseURL:   config.APIBaseURL,
		omnaraAPIURL: runtimeConfig.OmnaraAPIURL,
	}, nil
}

func (Definition) ResolveMachineProviderOptions(
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	return provideroptions.Merge(defaultOptions, projectOptions, agentOptions)
}

func (definition Definition) ValidatePool(policy executionstore.MachinePoolProviderPolicy) error {
	if err := providers.ValidateMachinePoolResourcePolicy(
		providers.Arker,
		policy,
		definition.ResourcePolicy(),
	); err != nil {
		return err
	}
	defaultOptions, err := parseProviderOptions(policy.DefaultProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	config, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	return validateAgainstAllowlists(config, defaultOptions, defaultOptions)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Arker,
		machineProvisioning,
		definition.ResourcePolicy(),
	); err != nil {
		return err
	}
	defaultOptions, err := parseProviderOptions(policy.DefaultProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	machineOptions, err := parseProviderOptions(machineProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	config, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	return validateAgainstAllowlists(config, machineOptions, defaultOptions)
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

func validateAgainstAllowlists(
	config providerConfig,
	options providerOptions,
	defaultOptions providerOptions,
) error {
	if err := providers.ValidateAllowedValue(
		"arker source",
		"allowed_sources",
		options.Source,
		config.AllowedSources,
		defaultOptions.Source,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"arker region",
		"allowed_regions",
		options.Region,
		config.AllowedRegions,
		defaultOptions.Region,
	)
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode arker provider config: %w", err)
	}
	config.APIBaseURL = strings.TrimSpace(config.APIBaseURL)
	var err error
	if config.APIBaseURL != "" {
		if config.APIBaseURL, err = normalizeAPIBaseURL(config.APIBaseURL); err != nil {
			return providerConfig{}, err
		}
	}
	if config.AllowedSources, err = providers.NormalizeAllowlist(
		"arker provider config allowed_sources",
		config.AllowedSources,
		validateIdentifier,
	); err != nil {
		return providerConfig{}, err
	}
	if config.AllowedRegions, err = providers.NormalizeAllowlist(
		"arker provider config allowed_regions",
		config.AllowedRegions,
		providers.ValidateDNSLabel,
	); err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func providerOptionsFromProvisioning(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if machineProvisioning.CPU == nil || *machineProvisioning.CPU <= 0 {
		return providerOptions{}, errors.New("arker machine config requires positive cpu")
	}
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("arker machine config requires positive memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("arker machine config requires provider_options")
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode arker provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode arker provider_options: %w", err)
	}
	options.Source = strings.TrimSpace(options.Source)
	if err := validateIdentifier(options.Source); err != nil {
		return providerOptions{}, fmt.Errorf("arker machine config source: %w", err)
	}
	options.Region = strings.TrimSpace(options.Region)
	if err := providers.ValidateDNSLabel(options.Region); err != nil {
		return providerOptions{}, fmt.Errorf("arker machine config region: %w", err)
	}
	if err := providers.ValidateManagedStartupScript("arker machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	if options.SleepAfterMS != 0 {
		if _, err := daemonprotocol.SleepAfterDuration(options.SleepAfterMS); err != nil {
			return providerOptions{}, fmt.Errorf("arker machine config sleep_after_ms %w", err)
		}
	}
	return options, nil
}

func validateIdentifier(value string) error {
	if value == "" {
		return errors.New("value must be non-empty")
	}
	if value == "*" {
		return errors.New(`value must not be "*"`)
	}
	if len(value) > 255 || strings.ContainsRune(value, 0) {
		return errors.New("value is invalid")
	}
	return nil
}

func normalizeAPIBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("arker api base url must be an absolute URL")
	}
	if !providers.IsHTTPS(parsed) {
		return "", errors.New("arker api base url must use https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("arker api base url must not include query or fragment")
	}
	return parsed.String(), nil
}

func existingMachineRegion(machineProvisioning executionstore.MachineProvisioningConfig) (string, error) {
	var region string
	if err := json.Unmarshal(machineProvisioning.ProviderOptions["region"], &region); err != nil ||
		providers.ValidateDNSLabel(strings.TrimSpace(region)) != nil {
		return "", errors.New("arker stored machine config requires a valid region")
	}
	return strings.TrimSpace(region), nil
}
