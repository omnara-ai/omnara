package novita

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// defaultDomain is the Novita Agent Sandbox region that serves both the
// control plane (api.<domain>) and the per-sandbox data plane.
const defaultDomain = "us-phx-1.sandbox.novita.ai"

var templatePattern = regexp.MustCompile(`^([a-z0-9_-]+/)?[a-z0-9_-]+$`)

type providerConfig struct {
	Domain           string   `json:"domain,omitempty"`
	AllowedTemplates []string `json:"allowed_templates,omitempty"`
}

type providerOptions struct {
	Template      string `json:"template"`
	StartupScript string `json:"startup_script,omitempty"`
}

type Definition struct{}

var _ providers.Definition = Definition{}
var _ providers.RuntimeProviderDefinition = Definition{}

func (Definition) ResourcePolicy() providers.MachineResourcePolicy {
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
	return newProvider(raw, runtimeConfig)
}

func (Definition) NewRuntimeProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (providers.RuntimeProvider, error) {
	return newProvider(raw, runtimeConfig)
}

func newProvider(raw json.RawMessage, runtimeConfig providers.RuntimeConfig) (*provider, error) {
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(runtimeConfig.ProviderAuthToken)
	if token == "" {
		return nil, errors.New("novita provider auth token is required")
	}
	return &provider{
		api: newRESTClient(
			config.Domain,
			token,
			providers.NewHTTPClient(runtimeConfig.HTTPRecorder, providers.Novita),
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

func (definition Definition) ValidatePool(policy executionstore.MachinePoolProviderPolicy) error {
	if err := providers.ValidateMachinePoolResourcePolicy(
		providers.Novita,
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
	return validateAllowedTemplate(defaultOptions, defaultOptions, config)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Novita,
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
	return validateAllowedTemplate(machineOptions, defaultOptions, config)
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

func validateAllowedTemplate(options, defaultOptions providerOptions, config providerConfig) error {
	return providers.ValidateAllowedValue(
		"novita template",
		"allowed_templates",
		options.Template,
		config.AllowedTemplates,
		defaultOptions.Template,
	)
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode novita provider config: %w", err)
	}
	config.Domain = strings.TrimSpace(config.Domain)
	if config.Domain == "" {
		config.Domain = defaultDomain
	}
	if err := validateDomain(config.Domain); err != nil {
		return providerConfig{}, fmt.Errorf("novita provider config domain: %w", err)
	}
	var err error
	config.AllowedTemplates, err = providers.NormalizeAllowlist(
		"novita provider config allowed_templates",
		config.AllowedTemplates,
		validateTemplate,
	)
	if err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("novita machine config requires provider_options")
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode novita provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode novita provider_options: %w", err)
	}
	options.Template = strings.TrimSpace(options.Template)
	if err := validateTemplate(options.Template); err != nil {
		return providerOptions{}, fmt.Errorf("novita machine config template: %w", err)
	}
	if err := providers.ValidateManagedStartupScript("novita machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	return options, nil
}

func validateTemplate(value string) error {
	if !templatePattern.MatchString(value) {
		return fmt.Errorf(
			"%q must be a template ID, name, or namespace/name of lowercase letters, numbers, hyphens, or underscores",
			value,
		)
	}
	return nil
}

func validateDomain(value string) error {
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q must be a domain such as %s", value, defaultDomain)
	}
	for _, label := range labels {
		if err := providers.ValidateDNSLabel(label); err != nil {
			return fmt.Errorf("%q must be a domain such as %s", value, defaultDomain)
		}
	}
	return nil
}
