package modal

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

const defaultEnvironment = "main"

var objectNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
var appIDPattern = regexp.MustCompile(`^ap-[a-zA-Z0-9]{22}$`)
var environmentNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]+$`)

type providerConfig struct {
	App            string   `json:"app"`
	Environment    string   `json:"environment,omitempty"`
	AllowedImages  []string `json:"allowed_images,omitempty"`
	AllowedRegions []string `json:"allowed_regions,omitempty"`
}

type providerCredential struct {
	TokenID     string `json:"token_id"`
	TokenSecret string `json:"token_secret"`
}

type providerOptions struct {
	Image         string `json:"image"`
	Region        string `json:"region,omitempty"`
	StartupScript string `json:"startup_script,omitempty"`
}

type Definition struct{}

var _ providers.Definition = Definition{}
var _ providers.RuntimeProviderDefinition = Definition{}

func resourcePolicy() providers.MachineResourcePolicy {
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
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	credential, err := parseProviderCredential(runtimeConfig.ProviderAuthToken)
	if err != nil {
		return nil, err
	}
	return &provider{
		app:          config.App,
		environment:  config.Environment,
		credential:   credential,
		omnaraAPIURL: runtimeConfig.OmnaraAPIURL,
	}, nil
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode modal provider config: %w", err)
	}
	config.App = strings.TrimSpace(config.App)
	if config.App == "" {
		config.App = "omnara"
	}
	if err := validateObjectName(config.App); err != nil {
		return providerConfig{}, fmt.Errorf("modal app: %w", err)
	}
	config.Environment = strings.TrimSpace(config.Environment)
	if config.Environment == "" {
		config.Environment = defaultEnvironment
	}
	if err := validateEnvironmentName(config.Environment); err != nil {
		return providerConfig{}, fmt.Errorf("modal environment: %w", err)
	}
	var err error
	config.AllowedImages, err = providers.NormalizeAllowlist(
		"modal provider config allowed_images",
		config.AllowedImages,
		providers.ValidateImageRef,
	)
	if err != nil {
		return providerConfig{}, err
	}
	config.AllowedRegions, err = providers.NormalizeAllowlist(
		"modal provider config allowed_regions",
		config.AllowedRegions,
		providers.ValidateDNSLabel,
	)
	if err != nil {
		return providerConfig{}, err
	}
	return config, nil
}

func parseProviderCredential(raw string) (providerCredential, error) {
	if strings.TrimSpace(raw) == "" {
		return providerCredential{}, errors.New("modal provider credentials are required")
	}
	var credential providerCredential
	if err := providers.DecodeStrictJSON(json.RawMessage(raw), &credential); err != nil {
		return providerCredential{}, fmt.Errorf("decode modal provider credentials: %w", err)
	}
	credential.TokenID = strings.TrimSpace(credential.TokenID)
	credential.TokenSecret = strings.TrimSpace(credential.TokenSecret)
	if credential.TokenID == "" || credential.TokenSecret == "" {
		return providerCredential{}, errors.New("modal provider credentials require token_id and token_secret")
	}
	return credential, nil
}

func (Definition) ResolveMachineProviderOptions(
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) map[string]json.RawMessage {
	return provideroptions.Merge(defaultOptions, projectOptions, agentOptions)
}

func (Definition) ValidatePool(policy executionstore.MachinePoolProviderPolicy) error {
	if err := providers.ValidateMachinePoolResourcePolicy(providers.Modal, policy, resourcePolicy()); err != nil {
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
	if err := providers.ValidateAllowedValue(
		"modal image",
		"allowed_images",
		defaultOptions.Image,
		config.AllowedImages,
		defaultOptions.Image,
	); err != nil {
		return err
	}
	return validateAllowedRegion(defaultOptions.Region, config.AllowedRegions, defaultOptions.Region)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.Modal,
		machineProvisioning,
		resourcePolicy(),
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
	if err := providers.ValidateAllowedValue(
		"modal image",
		"allowed_images",
		machineOptions.Image,
		config.AllowedImages,
		defaultOptions.Image,
	); err != nil {
		return err
	}
	return validateAllowedRegion(machineOptions.Region, config.AllowedRegions, defaultOptions.Region)
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
	if machineProvisioning.CPU == nil || *machineProvisioning.CPU <= 0 {
		return providerOptions{}, errors.New("modal machine config requires positive cpu")
	}
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("modal machine config requires positive memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("modal machine config requires provider_options")
	}
	var options providerOptions
	if err := providers.DecodeStringOptions(
		rawOptions,
		"modal provider_options",
		map[string]*string{
			"image":          &options.Image,
			"region":         &options.Region,
			"startup_script": &options.StartupScript,
		},
	); err != nil {
		return providerOptions{}, err
	}
	options.Image = strings.TrimSpace(options.Image)
	if err := providers.ValidateImageRef(options.Image); err != nil {
		return providerOptions{}, fmt.Errorf("modal machine config image: %w", err)
	}
	options.Region = strings.TrimSpace(options.Region)
	if options.Region != "" {
		if err := providers.ValidateDNSLabel(options.Region); err != nil {
			return providerOptions{}, fmt.Errorf("modal machine config region: %w", err)
		}
	}
	if err := providers.ValidateManagedStartupScript("modal machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	return options, nil
}

func validateAllowedRegion(value string, allowed []string, defaultValue string) error {
	err := providers.ValidateAllowedValue(
		"modal region",
		"allowed_regions",
		value,
		allowed,
		defaultValue,
	)
	if err != nil && value == "" {
		return errors.New("modal automatic region placement is not allowed by provider_config.allowed_regions")
	}
	return err
}

func validateObjectName(value string) error {
	if len(value) > 64 || !objectNamePattern.MatchString(value) ||
		appIDPattern.MatchString(value) {
		return errors.New("must contain at most 64 letters, numbers, dashes, periods, or underscores")
	}
	return nil
}

func validateEnvironmentName(value string) error {
	if len(value) > 64 || !environmentNamePattern.MatchString(value) ||
		strings.HasPrefix(strings.ToLower(value), "en-") {
		return errors.New(
			"must start with a letter or number, contain at most 64 letters, numbers, " +
				"dashes, periods, or underscores, and not start with en-",
		)
	}
	return nil
}
