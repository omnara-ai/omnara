package groundbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	minCPU             = 1
	maxCPU             = 16
	mebibytesPerGB     = 1024
	maxMemoryGBPerCPU  = 4
	idleTimeoutSeconds = 30
)

var templatePattern = regexp.MustCompile(`^(tpl_[A-Za-z0-9]{8,40}|[a-z0-9][a-z0-9-]{0,62})(:[1-9][0-9]{0,5})?$`)

type providerConfig struct {
	AllowedTemplates []string `json:"allowed_templates,omitempty"`
}

type providerOptions struct {
	Template      string `json:"template,omitempty"`
	StartupScript string `json:"startup_script,omitempty"`
	SleepAfterMS  int    `json:"sleep_after_ms,omitempty"`
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
		return nil, errors.New("groundbox provider auth token is required")
	}
	return &provider{
		api: newRESTClient(
			defaultAPIBaseURL,
			apiKey,
			providers.NewHTTPClient(runtimeConfig.HTTPRecorder, providers.Groundbox),
			providers.ObserveHTTPClient(lifecycleHTTPClient, runtimeConfig.HTTPRecorder, providers.Groundbox),
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
		providers.Groundbox,
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
		providers.Groundbox,
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
		return providerOptions{}, errors.New("groundbox machine config requires cpu and memory_mb")
	}
	if *cpu < minCPU || *cpu > maxCPU {
		return providerOptions{}, fmt.Errorf(
			"groundbox machines have %d to %d vCPU, not %d",
			minCPU,
			maxCPU,
			*cpu,
		)
	}
	if *memoryMB < mebibytesPerGB || *memoryMB%mebibytesPerGB != 0 ||
		*memoryMB > *cpu*maxMemoryGBPerCPU*mebibytesPerGB {
		return providerOptions{}, fmt.Errorf(
			"groundbox machine memory_mb must be whole GB (a multiple of %d) from 1 GB to %d GB per vCPU, not %d",
			mebibytesPerGB,
			maxMemoryGBPerCPU,
			*memoryMB,
		)
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func validateAllowedTemplate(options, defaultOptions providerOptions, config providerConfig) error {
	if options.Template == "" {
		return nil
	}
	return providers.ValidateAllowedValue(
		"groundbox template",
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
		return providerConfig{}, fmt.Errorf("decode groundbox provider config: %w", err)
	}
	var err error
	config.AllowedTemplates, err = providers.NormalizeAllowlist(
		"groundbox provider config allowed_templates",
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
		return providerOptions{}, fmt.Errorf("encode groundbox provider_options: %w", err)
	}
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode groundbox provider_options: %w", err)
	}
	options.Template = strings.TrimSpace(options.Template)
	if _, ok := rawOptions["template"]; ok && options.Template == "" {
		return providerOptions{}, errors.New(
			"groundbox machine config template must be non-empty; omit it to use the team's default template",
		)
	}
	if options.Template != "" {
		if err := validateTemplate(options.Template); err != nil {
			return providerOptions{}, fmt.Errorf("groundbox machine config template: %w", err)
		}
	}
	if err := providers.ValidateManagedStartupScript("groundbox machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	if options.SleepAfterMS != 0 {
		if _, err := daemonprotocol.SleepAfterDuration(options.SleepAfterMS); err != nil {
			return providerOptions{}, fmt.Errorf("groundbox machine config sleep_after_ms %w", err)
		}
	}
	return options, nil
}

func (o providerOptions) idleTimeoutSeconds() int {
	if o.SleepAfterMS == 0 {
		return 0
	}
	return idleTimeoutSeconds
}

func validateTemplate(value string) error {
	if !templatePattern.MatchString(value) {
		return fmt.Errorf(
			"%q must be a template id (tpl_...) or a name of lowercase letters, numbers, and hyphens, "+
				"optionally with a version such as name:3",
			value,
		)
	}
	return nil
}
