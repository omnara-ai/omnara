package agent37

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	defaultTemplate = "agent37-omnara"
	maxDiskGB       = 80
)

// Agent37 sells these vCPU and memory pairs; disk is sized separately.
var machineShapes = []machineShape{
	{CPU: 2, MemoryMB: 4096},
	{CPU: 4, MemoryMB: 8192},
	{CPU: 4, MemoryMB: 16384},
	{CPU: 4, MemoryMB: 32768},
}

type machineShape struct {
	CPU      int
	MemoryMB int
}

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
		return nil, errors.New("agent37 provider auth token is required")
	}
	return &provider{
		api:          newRESTClient(defaultAPIBaseURL, apiKey, nil, nil),
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
		providers.Agent37,
		policy,
		definition.ResourcePolicy(),
	); err != nil {
		return err
	}
	defaultOptions, err := providerOptionsFromProvisioning(policy.DefaultProvisioning)
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
		providers.Agent37,
		machineProvisioning,
		definition.ResourcePolicy(),
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

func providerOptionsFromProvisioning(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	cpu, memoryMB := machineProvisioning.CPU, machineProvisioning.MemoryMB
	if cpu == nil || memoryMB == nil {
		return providerOptions{}, errors.New("agent37 machine config requires cpu and memory_mb")
	}
	if !slices.Contains(machineShapes, machineShape{CPU: *cpu, MemoryMB: *memoryMB}) {
		return providerOptions{}, fmt.Errorf(
			"agent37 machines must be 2 vCPU with 4096 MB, or 4 vCPU with 8192, 16384, or 32768 MB, not %d vCPU with %d MB",
			*cpu,
			*memoryMB,
		)
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func validateAllowedTemplate(options, defaultOptions providerOptions, config providerConfig) error {
	return providers.ValidateAllowedValue(
		"agent37 template",
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
		return providerConfig{}, fmt.Errorf("decode agent37 provider config: %w", err)
	}
	var err error
	config.AllowedTemplates, err = providers.NormalizeAllowlist(
		"agent37 provider config allowed_templates",
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
		return providerOptions{}, fmt.Errorf("encode agent37 provider_options: %w", err)
	}
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode agent37 provider_options: %w", err)
	}
	options.Template = strings.TrimSpace(options.Template)
	if _, ok := rawOptions["template"]; ok && options.Template == "" {
		return providerOptions{}, errors.New(
			"agent37 machine config template must be non-empty; omit it to use " + defaultTemplate,
		)
	}
	if options.Template != "" {
		if err := validateTemplate(options.Template); err != nil {
			return providerOptions{}, fmt.Errorf("agent37 machine config template: %w", err)
		}
	}
	if _, ok := rawOptions["disk_gb"]; ok && (options.DiskGB < 1 || options.DiskGB > maxDiskGB) {
		return providerOptions{}, fmt.Errorf("agent37 machine config disk_gb must be between 1 and %d", maxDiskGB)
	}
	return options, providers.ValidateManagedStartupScript("agent37 machine config", options.StartupScript)
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
