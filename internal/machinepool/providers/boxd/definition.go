package boxd

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/provideroptions"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	// apiURL is the public boxd gRPC endpoint (https://boxd.sh/reference/grpc-api).
	apiURL = "boxd.sh:9443"
	// authURL exchanges an API key for the short-lived session token gRPC calls carry.
	authURL = "https://app.boxd.sh/api/v1/auth/token"
)

// supportedSizeClasses maps vCPU count to memory for the machine shapes boxd
// offers (1x4G, 2x8G, and 4x16G). Naming one dimension resolves its class.
var supportedSizeClasses = map[int]int{1: 4096, 2: 8192, 4: 16384}

type providerConfig struct {
	APIURL           string   `json:"api_url,omitempty"`
	AuthURL          string   `json:"auth_url,omitempty"`
	AllowedSnapshots []string `json:"allowed_snapshots,omitempty"`
}

type providerOptions struct {
	// Snapshot restores machines from a boxd snapshot; empty boots the base image.
	Snapshot      string `json:"snapshot"`
	StartupScript string `json:"startup_script"`
	// SleepAfterMS parks the daemon after this much inactivity so boxd can
	// suspend the machine between turns. Zero keeps the machine running.
	SleepAfterMS int `json:"sleep_after_ms"`
	// AutoSuspendSecs is how long boxd waits for the parked daemon to go quiet
	// before suspending the machine. Zero uses defaultAutoSuspendSecs. It trades
	// cost against wake latency and is only meaningful alongside sleep_after_ms.
	AutoSuspendSecs int `json:"auto_suspend_secs"`
}

const (
	// defaultAutoSuspendSecs is boxd's idle window for a pool machine that may
	// sleep, used when a pool does not choose one.
	defaultAutoSuspendSecs = 60
	// minimumAutoSuspendSecs keeps the window clear of the daemon's ten-second
	// heartbeat, so boxd never suspends a machine that is still working.
	minimumAutoSuspendSecs = 30
)

type Definition struct{}

var _ providers.RuntimeProviderDefinition = Definition{}

// resourcePolicy lets pools omit a default size: an omitted size resolves to
// the snapshot's size or the boxd org default at provisioning time.
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
	if strings.TrimSpace(runtimeConfig.ProviderAuthToken) == "" {
		return nil, errors.New("boxd provider auth token is required")
	}
	return &provider{
		api:          newGRPCClient(config.APIURL, config.AuthURL, runtimeConfig.ProviderAuthToken),
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

func (Definition) ValidatePool(
	policy executionstore.MachinePoolProviderPolicy,
) error {
	if err := providers.ValidateMachinePoolResourcePolicy(providers.Boxd, policy, resourcePolicy()); err != nil {
		return err
	}
	if err := validateConfiguredSize(
		"boxd machine pool default",
		policy.DefaultProvisioning.CPU,
		policy.DefaultProvisioning.MemoryMB,
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
		resourcePolicy(),
	); err != nil {
		return err
	}
	if err := validateConfiguredSize(
		"boxd machine config",
		machineProvisioning.CPU,
		machineProvisioning.MemoryMB,
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

// BuildMachineProvisioningIntent keeps a configured size as the requested
// machine shape; PrepareProvisioning resolves an omitted size later.
func (definition Definition) BuildMachineProvisioningIntent(
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := definition.ValidateMachineProvisioning(policy, machineProvisioning); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

// validateAllowedSnapshot enforces provider_config.allowed_snapshots. The base
// image (an empty snapshot) is always allowed because it is not a snapshot.
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

func validateConfiguredSize(what string, cpu, memoryMB *int) error {
	if cpu == nil && memoryMB == nil {
		return nil
	}
	if cpu != nil && memoryMB != nil {
		return validateSizeClass(what, *cpu, *memoryMB)
	}
	if cpu != nil {
		if _, ok := supportedSizeClasses[*cpu]; !ok {
			return fmt.Errorf("%s cpu=%d is not a supported boxd size class (%s)", what, *cpu, sizeClassList())
		}
		return nil
	}
	if _, ok := sizeClassForMemory(*memoryMB); !ok {
		return fmt.Errorf(
			"%s memory_mb=%d is not a supported boxd size class (%s)",
			what,
			*memoryMB,
			sizeClassList(),
		)
	}
	return nil
}

func validateSizeClass(what string, cpu, memoryMB int) error {
	if expectedMemoryMB, ok := supportedSizeClasses[cpu]; !ok || expectedMemoryMB != memoryMB {
		return fmt.Errorf(
			"%s cpu=%d memory_mb=%d is not a supported boxd size class (%s)",
			what,
			cpu,
			memoryMB,
			sizeClassList(),
		)
	}
	return nil
}

func sizeClassForMemory(memoryMB int) (int, bool) {
	for cpu, classMemoryMB := range supportedSizeClasses {
		if classMemoryMB == memoryMB {
			return cpu, true
		}
	}
	return 0, false
}

func sizeClassList() string {
	return "1 cpu with 4096 memory_mb, 2 with 8192, or 4 with 16384"
}

func parseProviderConfig(raw json.RawMessage) (providerConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var config providerConfig
	if err := providers.DecodeStrictJSON(raw, &config); err != nil {
		return providerConfig{}, fmt.Errorf("decode boxd provider config: %w", err)
	}
	normalizedAPIURL, err := normalizeAPIURL(config.APIURL)
	if err != nil {
		return providerConfig{}, err
	}
	config.APIURL = normalizedAPIURL
	normalizedAuthURL, err := normalizeAuthURL(config.AuthURL)
	if err != nil {
		return providerConfig{}, err
	}
	config.AuthURL = normalizedAuthURL
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
	if err := validateAutoSuspendSecs(options); err != nil {
		return providerOptions{}, err
	}
	return options, nil
}

func validateAutoSuspendSecs(options providerOptions) error {
	if options.AutoSuspendSecs == 0 {
		return nil
	}
	if options.AutoSuspendSecs < minimumAutoSuspendSecs || options.AutoSuspendSecs > math.MaxInt32 {
		return fmt.Errorf(
			"boxd machine config auto_suspend_secs must be between %d and %d",
			minimumAutoSuspendSecs,
			math.MaxInt32,
		)
	}
	if options.SleepAfterMS == 0 {
		return errors.New(
			"boxd machine config auto_suspend_secs requires sleep_after_ms, " +
				"because a machine that suspends without the daemon parking first cannot be woken",
		)
	}
	return nil
}

// autoSuspendSecs is the idle window boxd applies to a machine that may sleep.
func (o providerOptions) autoSuspendSecs() uint32 {
	if o.SleepAfterMS == 0 {
		return 0
	}
	if o.AutoSuspendSecs == 0 {
		return defaultAutoSuspendSecs
	}
	return uint32(o.AutoSuspendSecs)
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

// normalizeAPIURL accepts a bare host:port gRPC endpoint; TLS is always used.
func normalizeAPIURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return apiURL, nil
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/?#") {
		return "", errors.New("boxd api url must be a host:port endpoint without a scheme or path")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" {
		return "", errors.New("boxd api url must be a host:port endpoint")
	}
	if portNumber, err := strconv.Atoi(port); err != nil || portNumber <= 0 || portNumber > 65535 {
		return "", errors.New("boxd api url must use a numeric port")
	}
	return net.JoinHostPort(host, port), nil
}

func normalizeAuthURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return authURL, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("boxd auth url must be an absolute URL")
	}
	if !providers.IsHTTPS(parsed) {
		return "", errors.New("boxd auth url must use https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("boxd auth url must not include credentials, a query, or a fragment")
	}
	return parsed.String(), nil
}
