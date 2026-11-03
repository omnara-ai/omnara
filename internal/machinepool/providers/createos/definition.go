package createos

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const apiBaseURL = "https://api.sb.createos.sh"

type providerConfig struct {
	AllowedShapes   []string `json:"allowed_shapes,omitempty"`
	AllowedRootFSes []string `json:"allowed_rootfses,omitempty"`
}

type providerOptions struct {
	Shape         string `json:"shape"`
	RootFS        string `json:"rootfs"`
	StartupScript string `json:"startup_script,omitempty"`
	SleepAfterMS  int    `json:"sleep_after_ms,omitempty"`
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

func newProvider(
	raw json.RawMessage,
	runtimeConfig providers.RuntimeConfig,
) (*provider, error) {
	if _, err := parseProviderConfig(raw); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(runtimeConfig.ProviderAuthToken)
	if token == "" {
		return nil, errors.New("createos provider auth token is required")
	}
	return &provider{
		api:          newRESTClient(apiBaseURL, token, nil),
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
		providers.CreateOS,
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
	return validateAllowedOptions(defaultOptions, defaultOptions, config)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.CreateOS,
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
	return validateAllowedOptions(machineOptions, defaultOptions, config)
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

func validateAllowedOptions(options, defaultOptions providerOptions, config providerConfig) error {
	if err := providers.ValidateAllowedValue(
		"createos shape",
		"allowed_shapes",
		options.Shape,
		config.AllowedShapes,
		defaultOptions.Shape,
	); err != nil {
		return err
	}
	return providers.ValidateAllowedValue(
		"createos rootfs",
		"allowed_rootfses",
		options.RootFS,
		config.AllowedRootFSes,
		defaultOptions.RootFS,
	)
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode createos provider config: %w", err)
	}
	var err error
	config.AllowedShapes, err = providers.NormalizeAllowlist(
		"createos provider config allowed_shapes",
		config.AllowedShapes,
		validateShape,
	)
	if err != nil {
		return providerConfig{}, err
	}
	config.AllowedRootFSes, err = providers.NormalizeAllowlist(
		"createos provider config allowed_rootfses",
		config.AllowedRootFSes,
		providers.ValidateImageRef,
	)
	if err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("createos machine config requires provider_options")
	}
	raw, err := json.Marshal(rawOptions)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode createos provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(raw, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode createos provider_options: %w", err)
	}
	options.Shape = strings.TrimSpace(options.Shape)
	if err := validateShape(options.Shape); err != nil {
		return providerOptions{}, fmt.Errorf("createos machine config shape: %w", err)
	}
	options.RootFS = strings.TrimSpace(options.RootFS)
	if err := providers.ValidateImageRef(options.RootFS); err != nil {
		return providerOptions{}, fmt.Errorf("createos machine config rootfs: %w", err)
	}
	if base64.StdEncoding.EncodedLen(len(options.StartupScript)) > maxSandboxEnvValueBytes {
		return providerOptions{}, fmt.Errorf(
			"createos machine config startup_script must be at most %d bytes",
			base64.StdEncoding.DecodedLen(maxSandboxEnvValueBytes),
		)
	}
	if options.SleepAfterMS != 0 {
		if _, err := daemonprotocol.SleepAfterDuration(options.SleepAfterMS); err != nil {
			return providerOptions{}, fmt.Errorf("createos machine config sleep_after_ms %w", err)
		}
	}
	return options, nil
}

func validateShape(value string) error {
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
