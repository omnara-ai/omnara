package freestyle

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const apiBaseURL = "https://api.freestyle.sh/v5"

type providerConfig struct {
	AllowedSnapshots []string `json:"allowed_snapshots,omitempty"`
}

type providerOptions struct {
	Snapshot      string `json:"snapshot"`
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

func newProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (*provider, error) {
	if _, err := parseProviderConfig(raw); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(runtimeConfig.ProviderAuthToken)
	if token == "" {
		return nil, errors.New("freestyle provider auth token is required")
	}
	return &provider{
		api:          newRESTClient(apiBaseURL, token, nil),
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
		providers.Freestyle,
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
	return providers.ValidateAllowedValue(
		"freestyle snapshot",
		"allowed_snapshots",
		defaultOptions.Snapshot,
		config.AllowedSnapshots,
		defaultOptions.Snapshot,
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
		providers.Freestyle,
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
	return providers.ValidateAllowedValue(
		"freestyle snapshot",
		"allowed_snapshots",
		machineOptions.Snapshot,
		config.AllowedSnapshots,
		defaultOptions.Snapshot,
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

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode freestyle provider config: %w", err)
	}
	var err error
	config.AllowedSnapshots, err = providers.NormalizeAllowlist(
		"freestyle provider config allowed_snapshots",
		config.AllowedSnapshots,
		validateSnapshot,
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
		return providerOptions{}, errors.New("freestyle machine config requires positive cpu")
	}
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("freestyle machine config requires positive memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("freestyle machine config requires provider_options")
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode freestyle provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode freestyle provider_options: %w", err)
	}
	options.Snapshot = strings.TrimSpace(options.Snapshot)
	if err := validateSnapshot(options.Snapshot); err != nil {
		return providerOptions{}, fmt.Errorf("freestyle machine config snapshot: %w", err)
	}
	if err := providers.ValidateManagedStartupScript(
		"freestyle machine config",
		options.StartupScript,
	); err != nil {
		return providerOptions{}, err
	}
	if options.SleepAfterMS != 0 {
		if _, err := daemonprotocol.SleepAfterDuration(options.SleepAfterMS); err != nil {
			return providerOptions{}, fmt.Errorf("freestyle machine config sleep_after_ms %w", err)
		}
	}
	return options, nil
}

func validateSnapshot(value string) error {
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
