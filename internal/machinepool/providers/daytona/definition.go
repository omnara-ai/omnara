package daytona

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const apiBaseURL = "https://app.daytona.io/api"

type providerConfig struct {
	APIBaseURL       string   `json:"api_base_url,omitempty"`
	AllowedSnapshots []string `json:"allowed_snapshots,omitempty"`
	AllowedTargets   []string `json:"allowed_targets,omitempty"`
}

type providerOptions struct {
	Snapshot      string `json:"snapshot"`
	Target        string `json:"target"`
	StartupScript string `json:"startup_script"`
}

type Definition struct{}

func resourcePolicy() providers.MachineResourcePolicy {
	return providers.MachineResourcePolicy{
		CPU: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceOptional,
			Limits:       providers.MachineResourceRequired,
			Provisioning: providers.MachineResourceProviderResolved,
		},
		MemoryMB: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceOptional,
			Limits:       providers.MachineResourceRequired,
			Provisioning: providers.MachineResourceProviderResolved,
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
		return nil, errors.New("daytona provider auth token is required")
	}
	return &provider{
		api:             newRESTClient(config.APIBaseURL, runtimeConfig.ProviderAuthToken, nil),
		omnaraPublicURL: runtimeConfig.PublicURL,
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
	if err := providers.ValidateMachinePoolResourcePolicy(providers.Daytona, policy, resourcePolicy()); err != nil {
		return err
	}
	defaultOptions, err := parseProviderOptions(policy.DefaultProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	parsedProviderConfig, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	if err := providers.ValidateAllowedValue(
		"daytona snapshot",
		"allowed_snapshots",
		defaultOptions.Snapshot,
		parsedProviderConfig.AllowedSnapshots,
		defaultOptions.Snapshot,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"daytona target",
		"allowed_targets",
		defaultOptions.Target,
		parsedProviderConfig.AllowedTargets,
		defaultOptions.Target,
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
		providers.Daytona,
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
	parsedProviderConfig, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	if err := providers.ValidateAllowedValue(
		"daytona snapshot",
		"allowed_snapshots",
		machineOptions.Snapshot,
		parsedProviderConfig.AllowedSnapshots,
		defaultOptions.Snapshot,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"daytona target",
		"allowed_targets",
		machineOptions.Target,
		parsedProviderConfig.AllowedTargets,
		defaultOptions.Target,
	)
}

func (definition Definition) BuildMachineProvisioningIntent(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := definition.ValidateMachineProvisioning(policy, machineProvisioning); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	machineProvisioning.CPU = nil
	machineProvisioning.MemoryMB = nil
	return machineProvisioning, nil
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode daytona provider config: %w", err)
	}
	config.APIBaseURL = strings.TrimSpace(config.APIBaseURL)
	if config.APIBaseURL == "" {
		config.APIBaseURL = apiBaseURL
	}
	normalizedBaseURL, err := normalizeAPIBaseURL(config.APIBaseURL)
	if err != nil {
		return providerConfig{}, err
	}
	config.APIBaseURL = normalizedBaseURL
	config.AllowedSnapshots, err = providers.NormalizeAllowlist(
		"daytona provider config allowed_snapshots",
		config.AllowedSnapshots,
		validateIdentifier,
	)
	if err != nil {
		return providerConfig{}, err
	}
	config.AllowedTargets, err = providers.NormalizeAllowlist(
		"daytona provider config allowed_targets",
		config.AllowedTargets,
		validateIdentifier,
	)
	if err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func providerOptionsFromProvisioning(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if machineProvisioning.CPU == nil || *machineProvisioning.CPU <= 0 {
		return providerOptions{}, errors.New("daytona machine config requires positive cpu")
	}
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("daytona machine config requires positive memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(
	rawOptions map[string]json.RawMessage,
) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("daytona machine config requires provider_options")
	}
	var options providerOptions
	if err := providers.DecodeStringOptions(
		rawOptions,
		"daytona provider_options",
		map[string]*string{
			"snapshot":       &options.Snapshot,
			"target":         &options.Target,
			"startup_script": &options.StartupScript,
		},
	); err != nil {
		return providerOptions{}, err
	}
	options.Snapshot = strings.TrimSpace(options.Snapshot)
	if err := validateIdentifier(options.Snapshot); err != nil {
		return providerOptions{}, fmt.Errorf("daytona machine config snapshot: %w", err)
	}
	options.Target = strings.TrimSpace(options.Target)
	if err := validateIdentifier(options.Target); err != nil {
		return providerOptions{}, fmt.Errorf("daytona machine config target: %w", err)
	}
	if err := providers.ValidateManagedStartupScript("daytona machine config", options.StartupScript); err != nil {
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

func normalizeAPIBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("daytona api base url must be an absolute URL")
	}
	if !providers.IsHTTPS(parsed) {
		return "", errors.New("daytona api base url must use https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("daytona api base url must not include query or fragment")
	}
	return parsed.String(), nil
}

func positiveWholeNumber(value float64, what string) (int, error) {
	rounded := math.Round(value)
	if rounded <= 0 || math.Abs(value-rounded) > 0.000001 || rounded > math.MaxInt32 {
		return 0, fmt.Errorf("daytona %s must be a positive whole number", what)
	}
	return int(rounded), nil
}
