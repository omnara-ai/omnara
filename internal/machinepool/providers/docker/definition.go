package docker

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

var runtimeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type providerConfig struct {
	APIBaseURL    string   `json:"api_base_url"`
	AllowedImages []string `json:"allowed_images,omitempty"`
	Runtime       string   `json:"runtime,omitempty"`
}

type providerCredential struct {
	CA   string `json:"ca"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type providerOptions struct {
	Image         string `json:"image"`
	StartupScript string `json:"startup_script,omitempty"`
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
	config, err := parseProviderConfig(raw)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := parseProviderCredential(runtimeConfig.ProviderAuthToken)
	if err != nil {
		return nil, err
	}
	httpClient := providers.ObserveHTTPClient(
		providers.NewTLSHTTPClient(runtimeConfig.EndpointAllowlist, tlsConfig),
		runtimeConfig.HTTPRecorder,
		providers.Docker,
	)
	return &provider{
		api:          &restClient{baseURL: config.APIBaseURL + "/" + apiVersion, httpClient: httpClient},
		runtime:      config.Runtime,
		omnaraAPIURL: runtimeConfig.OmnaraAPIURL,
	}, nil
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode docker provider config: %w", err)
	}
	var err error
	config.APIBaseURL, err = normalizeAPIBaseURL(strings.TrimSpace(config.APIBaseURL))
	if err != nil {
		return providerConfig{}, err
	}
	config.AllowedImages, err = providers.NormalizeAllowlist(
		"docker provider config allowed_images",
		config.AllowedImages,
		providers.ValidateImageRef,
	)
	if err != nil {
		return providerConfig{}, err
	}
	config.Runtime = strings.TrimSpace(config.Runtime)
	if config.Runtime != "" && !runtimeNamePattern.MatchString(config.Runtime) {
		return providerConfig{}, errors.New(
			"docker provider config runtime must be a Docker runtime name such as runsc",
		)
	}
	return config, nil
}

func normalizeAPIBaseURL(baseURL string) (string, error) {
	if baseURL == "" {
		return "", errors.New("docker provider config requires api_base_url, such as https://docker-host:2376")
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("docker api base url must be an absolute URL")
	}
	if !providers.IsHTTPS(parsed) {
		return "", errors.New("docker api base url must use https")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("docker api base url must not include credentials, a path, query, or fragment")
	}
	return parsed.String(), nil
}

func parseProviderCredential(raw string) (*tls.Config, error) {
	const format = `docker credentials must be JSON like ` +
		`{"ca":"-----BEGIN CERTIFICATE-----...","cert":"-----BEGIN CERTIFICATE-----...","key":"-----BEGIN PRIVATE KEY-----..."}`
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("docker provider credentials are required")
	}
	var credential providerCredential
	if err := providers.DecodeStrictJSON(json.RawMessage(raw), &credential); err != nil {
		return nil, errors.New(format)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(credential.CA)) {
		return nil, errors.New("docker credentials ca must be a PEM certificate")
	}
	certificate, err := tls.X509KeyPair([]byte(credential.Cert), []byte(credential.Key))
	if err != nil {
		return nil, fmt.Errorf("docker credentials cert and key must be a matching PEM pair: %w", err)
	}
	return &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
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
		providers.Docker, policy, definition.ResourcePolicy(),
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
		"docker image",
		"allowed_images",
		defaultOptions.Image,
		config.AllowedImages,
		defaultOptions.Image,
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
		providers.Docker,
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
		"docker image",
		"allowed_images",
		machineOptions.Image,
		config.AllowedImages,
		defaultOptions.Image,
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

func providerOptionsFromProvisioning(
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providerOptions, error) {
	if machineProvisioning.CPU == nil || *machineProvisioning.CPU <= 0 {
		return providerOptions{}, errors.New("docker machine config requires positive cpu")
	}
	if machineProvisioning.MemoryMB == nil || *machineProvisioning.MemoryMB <= 0 {
		return providerOptions{}, errors.New("docker machine config requires positive memory_mb")
	}
	return parseProviderOptions(machineProvisioning.ProviderOptions)
}

func parseProviderOptions(rawOptions map[string]json.RawMessage) (providerOptions, error) {
	if rawOptions == nil {
		return providerOptions{}, errors.New("docker machine config requires provider_options")
	}
	var options providerOptions
	if err := providers.DecodeStringOptions(
		rawOptions,
		"docker provider_options",
		map[string]*string{
			"image":          &options.Image,
			"startup_script": &options.StartupScript,
		},
	); err != nil {
		return providerOptions{}, err
	}
	options.Image = strings.TrimSpace(options.Image)
	if err := providers.ValidateImageRef(options.Image); err != nil {
		return providerOptions{}, fmt.Errorf("docker machine config image: %w", err)
	}
	if err := providers.ValidateManagedStartupScript("docker machine config", options.StartupScript); err != nil {
		return providerOptions{}, err
	}
	return options, nil
}
