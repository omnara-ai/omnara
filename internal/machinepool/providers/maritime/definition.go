package maritime

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

const (
	defaultTemplate = "base"
	minCPU          = 1
	maxCPU          = 16
	minMemoryMiB    = 1024
	maxMemoryMiB    = 65536
	minDiskGiB      = 5
	maxDiskGiB      = 500
)

// templatePattern accepts Maritime snapshot and template names and public OCI
// image references, which Maritime resolves in that order.
var templatePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,511}$`)

type providerConfig struct {
	AllowedTemplates []string `json:"allowed_templates,omitempty"`
}

type providerOptions struct {
	Template      string `json:"template,omitempty"`
	DiskGB        int    `json:"disk_gb,omitempty"`
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

func newProvider(raw json.RawMessage, runtimeConfig providers.RuntimeConfig) (*provider, error) {
	if _, err := parseProviderConfig(raw); err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(runtimeConfig.ProviderAuthToken)
	if apiKey == "" {
		return nil, errors.New("maritime provider auth token is required")
	}
	return &provider{
		api: newRESTClient(
			defaultAPIBaseURL,
			apiKey,
			providers.NewHTTPClient(runtimeConfig.HTTPRecorder, providers.Maritime),
		),
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
		providers.Maritime,
		policy,
		definition.ResourcePolicy(),
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
	return validateAllowedTemplate(options, options, config)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Maritime,
		machineProvisioning,
		definition.ResourcePolicy(),
	); err != nil {
		return err
	}
	machineOptions, err := providerOptionsFromProvisioning(machineProvisioning)
	if err != nil {
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
	return validateAllowedTemplate(machineOptions, defaultOptions, config)
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

func validateAllowedTemplate(options, defaultOptions providerOptions, config providerConfig) error {
	return providers.ValidateAllowedValue(
		"maritime template",
		"allowed_templates",
		options.template(),
		config.AllowedTemplates,
		defaultOptions.template(),
	)
}

func providerOptionsFromProvisioning(
	config executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if config.CPU == nil || *config.CPU < minCPU || *config.CPU > maxCPU {
		return providerOptions{}, fmt.Errorf("maritime machine config cpu must be between %d and %d", minCPU, maxCPU)
	}
	if config.MemoryMB == nil || *config.MemoryMB < minMemoryMiB || *config.MemoryMB > maxMemoryMiB ||
		*config.MemoryMB%1024 != 0 {
		return providerOptions{}, fmt.Errorf(
			"maritime machine config memory_mb must be a multiple of 1024 between %d and %d",
			minMemoryMiB,
			maxMemoryMiB,
		)
	}
	return parseProviderOptions(config.ProviderOptions)
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode maritime provider config: %w", err)
	}
	var err error
	config.AllowedTemplates, err = providers.NormalizeAllowlist(
		"maritime provider config allowed_templates",
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
		return providerOptions{}, fmt.Errorf("encode maritime provider_options: %w", err)
	}
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode maritime provider_options: %w", err)
	}
	options.Template = strings.TrimSpace(options.Template)
	if _, ok := rawOptions["template"]; ok && options.Template == "" {
		return providerOptions{}, errors.New(
			"maritime machine config template must be non-empty; omit it to use " + defaultTemplate,
		)
	}
	if options.Template != "" {
		if err := validateTemplate(options.Template); err != nil {
			return providerOptions{}, fmt.Errorf("maritime machine config template: %w", err)
		}
	}
	if options.DiskGB != 0 && (options.DiskGB < minDiskGiB || options.DiskGB > maxDiskGiB) {
		return providerOptions{}, fmt.Errorf(
			"maritime machine config disk_gb must be between %d and %d",
			minDiskGiB,
			maxDiskGiB,
		)
	}
	return options, providers.ValidateManagedStartupScript("maritime machine config", options.StartupScript)
}

func validateTemplate(value string) error {
	switch {
	case value == "":
		return errors.New("value must be non-empty")
	case value == "*":
		return errors.New(`value must not be "*"`)
	case !templatePattern.MatchString(value):
		return errors.New("value must be a Maritime template or snapshot name, or a public image reference")
	}
	return nil
}
