// Package arker provisions pool machines as Arker VMs.
package arker

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

type providerConfig struct {
	BaseURL          string   `json:"base_url,omitempty"`
	ControlBaseURL   string   `json:"control_base_url,omitempty"`
	AllowedSources   []string `json:"allowed_sources,omitempty"`
	AllowedProviders []string `json:"allowed_providers,omitempty"`
	AllowedRegions   []string `json:"allowed_regions,omitempty"`
}

type providerOptions struct {
	Source        string `json:"source"`
	Provider      string `json:"provider"`
	Region        string `json:"region"`
	StartupScript string `json:"startup_script"`
}

type Definition struct{}

var _ providers.RuntimeProviderDefinition = Definition{}

// A fork is sized by the request, so cpu and memory are configured rather than
// provider-resolved.
func resourcePolicy() providers.MachineResourcePolicy {
	return providers.MachineResourcePolicy{
		CPU: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceOptional,
			Limits:       providers.MachineResourceRequired,
			Provisioning: providers.MachineResourceConfigured,
		},
		MemoryMB: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceOptional,
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

func (Definition) ResolveMachineProviderOptions(
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	return provideroptions.Merge(defaultOptions, projectOptions, agentOptions)
}

func (Definition) ValidatePool(policy executionstore.MachinePoolProviderPolicy) error {
	if err := providers.ValidateMachinePoolResourcePolicy(providers.Arker, policy, resourcePolicy()); err != nil {
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
	if err := validatePlacement(config, defaultOptions); err != nil {
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
		resourcePolicy(),
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
	if err := validatePlacement(config, machineOptions); err != nil {
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

// A pool configures one or the other; both would silently disagree about where
// a machine lives.
func validatePlacement(config providerConfig, options providerOptions) error {
	switch {
	case config.BaseURL != "" && options.Provider != "":
		return errors.New(
			"arker provider config base_url and machine config provider/region are mutually exclusive",
		)
	case config.BaseURL == "" && options.Provider == "":
		return errors.New(
			"arker requires a placement: set base_url in the provider config, or provider and region in provider_options",
		)
	}
	return nil
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
	if options.Provider == "" {
		return nil
	}
	if err := providers.ValidateAllowedValue(
		"arker placement provider",
		"allowed_providers",
		options.Provider,
		config.AllowedProviders,
		defaultOptions.Provider,
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
	var err error
	if config.BaseURL, err = normalizeOptionalURL("arker base_url", config.BaseURL); err != nil {
		return providerConfig{}, err
	}
	if config.ControlBaseURL, err = normalizeOptionalURL(
		"arker control_base_url",
		config.ControlBaseURL,
	); err != nil {
		return providerConfig{}, err
	}
	if config.AllowedSources, err = providers.NormalizeAllowlist(
		"arker provider config allowed_sources",
		config.AllowedSources,
		validateIdentifier,
	); err != nil {
		return providerConfig{}, err
	}
	// Placement values reach the API hostname, so they are DNS labels.
	if config.AllowedProviders, err = providers.NormalizeAllowlist(
		"arker provider config allowed_providers",
		config.AllowedProviders,
		providers.ValidateDNSLabel,
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
	var options providerOptions
	if err := providers.DecodeStringOptions(
		rawOptions,
		"arker provider_options",
		map[string]*string{
			"source":         &options.Source,
			"provider":       &options.Provider,
			"region":         &options.Region,
			"startup_script": &options.StartupScript,
		},
	); err != nil {
		return providerOptions{}, err
	}
	options.Source = strings.TrimSpace(options.Source)
	if err := validateIdentifier(options.Source); err != nil {
		return providerOptions{}, fmt.Errorf("arker machine config source: %w", err)
	}
	options.Provider = strings.TrimSpace(options.Provider)
	options.Region = strings.TrimSpace(options.Region)
	if (options.Provider == "") != (options.Region == "") {
		return providerOptions{}, errors.New(
			"arker machine config provider and region must be set together",
		)
	}
	if options.Provider != "" {
		if err := providers.ValidateDNSLabel(options.Provider); err != nil {
			return providerOptions{}, fmt.Errorf("arker machine config provider: %w", err)
		}
		if err := providers.ValidateDNSLabel(options.Region); err != nil {
			return providerOptions{}, fmt.Errorf("arker machine config region: %w", err)
		}
	}
	if err := providers.ValidateManagedStartupScript("arker machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
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

func normalizeOptionalURL(what, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(strings.TrimRight(value, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%s must be an absolute URL", what)
	}
	if !providers.IsHTTPS(parsed) {
		return "", fmt.Errorf("%s must use https", what)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must not include query or fragment", what)
	}
	return parsed.String(), nil
}
