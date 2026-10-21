package superserve

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const defaultTemplate = "superserve/omnara"

type providerConfig struct {
	AllowedTemplates []string `json:"allowed_templates,omitempty"`
}

type providerOptions struct {
	Template      string `json:"template,omitempty"`
	StartupScript string `json:"startup_script,omitempty"`
}

func (o providerOptions) template() string {
	if o.Template == "" {
		return defaultTemplate
	}
	return o.Template
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
	if _, err := parseProviderConfig(raw); err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(runtimeConfig.ProviderAuthToken)
	if apiKey == "" {
		return nil, errors.New("superserve provider auth token is required")
	}
	return &provider{
		api:          newRESTClient(endpointsForAPIKey(apiKey), apiKey, nil),
		omnaraAPIURL: runtimeConfig.OmnaraAPIURL,
		pollDelay:    pollDelay,
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
		providers.Superserve,
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
		providers.Superserve,
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
		"superserve template",
		"allowed_templates",
		options.template(),
		config.AllowedTemplates,
		defaultOptions.template(),
	)
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode superserve provider config: %w", err)
	}
	var err error
	config.AllowedTemplates, err = providers.NormalizeAllowlist(
		"superserve provider config allowed_templates",
		config.AllowedTemplates,
		validateTemplate,
	)
	if err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	var options providerOptions
	if rawOptions == nil {
		return options, nil
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode superserve provider_options: %w", err)
	}
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode superserve provider_options: %w", err)
	}
	options.Template = strings.TrimSpace(options.Template)
	if _, ok := rawOptions["template"]; ok && options.Template == "" {
		return providerOptions{}, errors.New(
			"superserve machine config template must be non-empty; omit it to use " + defaultTemplate,
		)
	}
	if options.Template != "" {
		if err := validateTemplate(options.Template); err != nil {
			return providerOptions{}, fmt.Errorf("superserve machine config template: %w", err)
		}
	}
	return options, providers.ValidateManagedStartupScript("superserve machine config", options.StartupScript)
}

func validateTemplate(value string) error {
	switch {
	case value == "":
		return errors.New("value must be non-empty")
	case value == "*":
		return errors.New(`value must not be "*"`)
	case len(value) > 255 || strings.ContainsRune(value, 0) || strings.ContainsAny(value, "\r\n"):
		return errors.New("value is invalid")
	}
	return nil
}
