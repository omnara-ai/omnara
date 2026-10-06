package boxd

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

type providerConfig struct {
	AllowedSnapshots []string `json:"allowed_snapshots,omitempty"`
}

type providerOptions struct {
	Snapshot      string `json:"snapshot"`
	StartupScript string `json:"startup_script"`
	SleepAfterMS  int    `json:"sleep_after_ms"`
}

const autoSuspendTimeoutSecs = 60

type Definition struct{}

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

func newProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (*provider, error) {
	if _, err := parseProviderConfig(raw); err != nil {
		return nil, err
	}
	if strings.TrimSpace(runtimeConfig.ProviderAuthToken) == "" {
		return nil, errors.New("boxd provider auth token is required")
	}
	return &provider{
		api:          newGRPCClient(runtimeConfig.ProviderAuthToken),
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
		providers.Boxd, policy, definition.ResourcePolicy(),
	); err != nil {
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
	return validateAllowedSnapshot(defaultOptions.Snapshot, parsedProviderConfig, defaultOptions.Snapshot)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Boxd,
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
	parsedProviderConfig, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	return validateAllowedSnapshot(machineOptions.Snapshot, parsedProviderConfig, defaultOptions.Snapshot)
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

func validateAllowedSnapshot(snapshot string, config providerConfig, defaultSnapshot string) error {
	if snapshot == "" {
		return nil
	}
	return providers.ValidateAllowedValue(
		"boxd snapshot",
		"allowed_snapshots",
		snapshot,
		config.AllowedSnapshots,
		defaultSnapshot,
	)
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode boxd provider config: %w", err)
	}
	var err error
	config.AllowedSnapshots, err = providers.NormalizeAllowlist(
		"boxd provider config allowed_snapshots",
		config.AllowedSnapshots,
		validateSnapshotRef,
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
		return providerOptions{}, errors.New("boxd machine config requires positive cpu")
	}
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("boxd machine config requires positive memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(
	rawOptions map[string]json.RawMessage,
) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("boxd machine config requires provider_options")
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode boxd provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode boxd provider_options: %w", err)
	}
	options.Snapshot = strings.TrimSpace(options.Snapshot)
	if _, ok := rawOptions["snapshot"]; ok && options.Snapshot == "" {
		return providerOptions{}, errors.New("boxd machine config snapshot must be non-empty; omit it to use the base image")
	}
	if options.Snapshot != "" {
		if err := validateSnapshotRef(options.Snapshot); err != nil {
			return providerOptions{}, fmt.Errorf("boxd machine config snapshot: %w", err)
		}
	}
	if err := providers.ValidateManagedStartupScript("boxd machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	if options.SleepAfterMS != 0 {
		if _, err := daemonprotocol.SleepAfterDuration(options.SleepAfterMS); err != nil {
			return providerOptions{}, fmt.Errorf("boxd machine config sleep_after_ms %w", err)
		}
	}
	return options, nil
}

func (o providerOptions) autoSuspendSecs() uint32 {
	if o.SleepAfterMS == 0 {
		return 0
	}
	return autoSuspendTimeoutSecs
}

func validateSnapshotRef(value string) error {
	if value == "" {
		return errors.New("value must be non-empty")
	}
	if value == "*" {
		return errors.New(`value must not be "*"`)
	}
	if len(value) > 255 || strings.ContainsRune(value, 0) || strings.ContainsAny(value, " \t\r\n") {
		return errors.New("value is invalid")
	}
	return nil
}
