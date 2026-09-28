package createos

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const defaultAPIBaseURL = "https://api.sb.createos.sh"

type providerConfig struct {
	APIBaseURL      string   `json:"api_base_url,omitempty"`
	AllowedShapes   []string `json:"allowed_shapes,omitempty"`
	AllowedRootFSes []string `json:"allowed_rootfses,omitempty"`
	AllowedRegions  []string `json:"allowed_regions,omitempty"`
}

type providerOptions struct {
	Shape         string `json:"shape"`
	RootFS        string `json:"rootfs"`
	Region        string `json:"region"`
	StartupScript string `json:"startup_script"`
}

type Definition struct{}

var _ providers.RuntimeProviderDefinition = Definition{}

func resourcePolicy() providers.MachineResourcePolicy {
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

func (Definition) ResourcePolicy() providers.MachineResourcePolicy {
	return resourcePolicy()
}

func (Definition) NewProvider(
	raw json.RawMessage,
	runtime providers.RuntimeConfig,
) (providers.Provider, error) {
	return newProvider(raw, runtime)
}

func (Definition) NewRuntimeProvider(
	raw json.RawMessage,
	runtime providers.RuntimeConfig,
) (providers.RuntimeProvider, error) {
	return newProvider(raw, runtime)
}

func newProvider(raw json.RawMessage, runtime providers.RuntimeConfig) (*provider, error) {
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(runtime.ProviderAuthToken) == "" {
		return nil, errors.New("createos provider auth token is required")
	}
	return &provider{
		api:          newRESTClient(config.APIBaseURL, runtime.ProviderAuthToken, nil),
		omnaraAPIURL: runtime.OmnaraAPIURL,
	}, nil
}

func (Definition) ResolveMachineProviderOptions(
	defaults, project, agent map[string]json.RawMessage,
) map[string]json.RawMessage {
	return provideroptions.Merge(defaults, project, agent)
}

func (Definition) ValidatePool(policy executionstore.MachinePoolProviderPolicy) error {
	if err := providers.ValidateMachinePoolResourcePolicy(providers.CreateOS, policy, resourcePolicy()); err != nil {
		return err
	}
	options, err := parseProviderOptions(policy.DefaultProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	config, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	return validateAllowedOptions(options, options, config)
}

func (definition Definition) ValidateMachineProvisioning(
	policy executionstore.MachinePoolProviderPolicy,
	provisioning executionstore.MachineProvisioningConfig,
) error {
	if err := definition.ValidatePool(policy); err != nil {
		return err
	}
	if err := providers.ValidateMachineProvisioningResourcePolicy(
		providers.CreateOS,
		provisioning,
		resourcePolicy(),
	); err != nil {
		return err
	}
	defaults, err := parseProviderOptions(policy.DefaultProvisioning.ProviderOptions)
	if err != nil {
		return err
	}
	options, err := parseProviderOptions(provisioning.ProviderOptions)
	if err != nil {
		return err
	}
	config, err := parseProviderConfig(policy.ProviderConfig)
	if err != nil {
		return err
	}
	return validateAllowedOptions(options, defaults, config)
}

func (definition Definition) BuildMachineProvisioningIntent(
	policy executionstore.MachinePoolProviderPolicy,
	provisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := definition.ValidateMachineProvisioning(policy, provisioning); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	provisioning.CPU = nil
	provisioning.MemoryMB = nil
	return provisioning, nil
}

func validateAllowedOptions(options, defaults providerOptions, config providerConfig) error {
	checks := []struct {
		what, field, value, fallback string
		allowed                      []string
	}{
		{"createos shape", "allowed_shapes", options.Shape, defaults.Shape, config.AllowedShapes},
		{"createos rootfs", "allowed_rootfses", options.RootFS, defaults.RootFS, config.AllowedRootFSes},
		{"createos region", "allowed_regions", options.Region, defaults.Region, config.AllowedRegions},
	}
	for _, check := range checks {
		if err := providers.ValidateAllowedValue(
			check.what,
			check.field,
			check.value,
			check.allowed,
			check.fallback,
		); err != nil {
			return err
		}
	}
	return nil
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode createos provider config: %w", err)
	}
	config.APIBaseURL = strings.TrimSpace(config.APIBaseURL)
	if config.APIBaseURL == "" {
		config.APIBaseURL = defaultAPIBaseURL
	}
	normalized, err := normalizeAPIBaseURL(config.APIBaseURL)
	if err != nil {
		return providerConfig{}, err
	}
	config.APIBaseURL = normalized
	config.AllowedShapes, err = providers.NormalizeAllowlist(
		"createos provider config allowed_shapes",
		config.AllowedShapes,
		providers.ValidateDNSLabel,
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
	config.AllowedRegions, err = providers.NormalizeAllowlist(
		"createos provider config allowed_regions",
		config.AllowedRegions,
		providers.ValidateDNSLabel,
	)
	return config, err
}

func parseProviderOptions(raw map[string]json.RawMessage) (providerOptions, error) {
	if raw == nil {
		return providerOptions{}, errors.New("createos machine config requires provider_options")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return providerOptions{}, fmt.Errorf("encode createos provider_options: %w", err)
	}
	var options providerOptions
	if err := providers.DecodeStrictJSON(encoded, &options); err != nil {
		return providerOptions{}, fmt.Errorf("decode createos provider_options: %w", err)
	}
	options.Shape = strings.TrimSpace(options.Shape)
	options.RootFS = strings.TrimSpace(options.RootFS)
	options.Region = strings.TrimSpace(options.Region)
	if err := providers.ValidateDNSLabel(options.Shape); err != nil {
		return providerOptions{}, fmt.Errorf("createos machine config shape: %w", err)
	}
	if err := providers.ValidateImageRef(options.RootFS); err != nil {
		return providerOptions{}, fmt.Errorf("createos machine config rootfs: %w", err)
	}
	if options.Region != "" {
		if err := providers.ValidateDNSLabel(options.Region); err != nil {
			return providerOptions{}, fmt.Errorf("createos machine config region: %w", err)
		}
	}
	if err := providers.ValidateManagedStartupScript("createos machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	return options, nil
}

func normalizeAPIBaseURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(value, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("createos api base url must be an absolute URL")
	}
	if !providers.IsHTTPS(parsed) {
		return "", errors.New("createos api base url must use https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("createos api base url must not include query or fragment")
	}
	return parsed.String(), nil
}
