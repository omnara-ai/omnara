package revyl

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	apiBaseURL  = "https://backend.revyl.ai"
	minLifetime = time.Minute
	maxLifetime = 24 * time.Hour
)

type providerConfig struct {
	APIBaseURL string `json:"api_base_url,omitempty"`
}

type providerOptions struct {
	Lifetime      time.Duration
	StartupScript string
}

type Definition struct{}

var _ providers.RuntimeProviderDefinition = Definition{}

func (Definition) ResourcePolicy() providers.MachineResourcePolicy {
	return providers.MachineResourcePolicy{
		CPU: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceUnsupported,
			Limits:       providers.MachineResourceUnsupported,
			Provisioning: providers.MachineResourceUnmodeled,
		},
		MemoryMB: providers.MachineResourceContract{
			PoolDefault:  providers.MachineResourceUnsupported,
			Limits:       providers.MachineResourceUnsupported,
			Provisioning: providers.MachineResourceUnmodeled,
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
		return nil, errors.New("revyl provider auth token is required")
	}
	return &provider{
		api: newRESTClient(
			config.APIBaseURL,
			runtimeConfig.ProviderAuthToken,
			providers.ObserveHTTPClient(runtimeConfig.HTTPClient, runtimeConfig.HTTPRecorder, providers.Revyl),
		),
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

func (definition Definition) ValidatePool(
	policy executionstore.MachinePoolProviderPolicy,
) error {
	if err := providers.ValidateMachinePoolResourcePolicy(
		providers.Revyl, policy, definition.ResourcePolicy(),
	); err != nil {
		return err
	}
	if _, err := parseProviderOptions(policy.DefaultProvisioning.ProviderOptions); err != nil {
		return err
	}
	_, err := parseProviderConfig(policy.ProviderConfig)
	return err
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Revyl,
		machineProvisioning,
		definition.ResourcePolicy(),
	); err != nil {
		return err
	}
	_, err := parseProviderOptions(machineProvisioning.ProviderOptions)
	return err
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
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode revyl provider config: %w", err)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.APIBaseURL), "/")
	if baseURL == "" {
		baseURL = apiBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || !providers.IsHTTPS(parsed) || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return providerConfig{}, errors.New(
			"revyl api base url must be an absolute https URL without credentials, query, or fragment",
		)
	}
	config.APIBaseURL = parsed.String()
	return config, nil
}

func providerOptionsFromProvisioning(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if machineProvisioning.CPU != nil || machineProvisioning.MemoryMB != nil {
		return providerOptions{}, errors.New("revyl machine config does not support cpu or memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if len(rawOptions) == 0 {
		return providerOptions{}, nil
	}
	var lifetime string
	var options providerOptions
	if err := providers.DecodeStringOptions(
		rawOptions,
		"revyl provider_options",
		map[string]*string{"lifetime": &lifetime, "startup_script": &options.StartupScript},
	); err != nil {
		return providerOptions{}, err
	}
	if lifetime = strings.TrimSpace(lifetime); lifetime != "" {
		parsed, err := time.ParseDuration(lifetime)
		if err != nil || parsed < minLifetime || parsed > maxLifetime || parsed%time.Second != 0 {
			return providerOptions{}, fmt.Errorf(
				"revyl machine config lifetime must be whole seconds between %s and %s, such as 2h",
				minLifetime,
				maxLifetime,
			)
		}
		options.Lifetime = parsed
	}
	if err := providers.ValidateManagedStartupScript("revyl machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	return options, nil
}
