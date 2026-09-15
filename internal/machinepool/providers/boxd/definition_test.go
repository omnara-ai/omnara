package boxd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestBoxdDefinitionCreatesProviderFromConfig(t *testing.T) {
	runtime, err := (Definition{}).NewProvider(
		json.RawMessage(`{"allowed_snapshots":["team-workspace"]}`),
		providers.RuntimeConfig{
			OmnaraAPIURL:      "https://api.omnara.test/v1",
			ProviderAuthToken: "bxd_token",
		},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	concrete, ok := runtime.(*provider)
	if !ok {
		t.Fatalf("provider type = %T, want *provider", runtime)
	}
	client, ok := concrete.api.(*grpcClient)
	if !ok {
		t.Fatalf("api client type = %T, want *grpcClient", concrete.api)
	}
	if client.apiURL != apiURL || client.authURL != authURL || client.apiKey != "bxd_token" {
		t.Fatalf("client config = api %q auth %q key %q", client.apiURL, client.authURL, client.apiKey)
	}
	if concrete.omnaraAPIURL != "https://api.omnara.test/v1" {
		t.Fatalf("omnara API URL = %q", concrete.omnaraAPIURL)
	}
}

func TestBoxdDefinitionCreatesProviderWithCustomEndpoints(t *testing.T) {
	runtime, err := (Definition{}).NewProvider(
		json.RawMessage(`{"api_url":" staging.boxd.example:9443 ","auth_url":"https://app.staging.boxd.example/api/v1/auth/token"}`),
		providers.RuntimeConfig{OmnaraAPIURL: "https://api.omnara.test/v1", ProviderAuthToken: "bxd_token"},
	)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	concrete, ok := runtime.(*provider)
	if !ok {
		t.Fatalf("provider type = %T, want *provider", runtime)
	}
	client, ok := concrete.api.(*grpcClient)
	if !ok {
		t.Fatalf("api client type = %T, want *grpcClient", concrete.api)
	}
	if client.apiURL != "staging.boxd.example:9443" ||
		client.authURL != "https://app.staging.boxd.example/api/v1/auth/token" {
		t.Fatalf("client endpoints = api %q auth %q", client.apiURL, client.authURL)
	}
}

func TestBoxdDefinitionRequiresAuthToken(t *testing.T) {
	_, err := (Definition{}).NewProvider(nil, providers.RuntimeConfig{OmnaraAPIURL: "https://api.omnara.test/v1"})
	if err == nil || !strings.Contains(err.Error(), "auth token is required") {
		t.Fatalf("missing token error = %v", err)
	}
}

func TestParseBoxdProviderConfigDefaults(t *testing.T) {
	config, err := parseProviderConfig(nil)
	if err != nil {
		t.Fatalf("parse empty boxd provider config: %v", err)
	}
	if config.APIURL != apiURL || config.AuthURL != authURL || config.AllowedSnapshots != nil {
		t.Fatalf("default boxd provider config = %+v", config)
	}
}

func TestParseBoxdProviderConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "api url with scheme", raw: `{"api_url":"https://boxd.sh:9443"}`, want: "host:port"},
		{name: "api url without port", raw: `{"api_url":"boxd.sh"}`, want: "host:port"},
		{name: "api url with path", raw: `{"api_url":"boxd.sh:9443/api"}`, want: "host:port"},
		{name: "api url non-numeric port", raw: `{"api_url":"boxd.sh:grpc"}`, want: "numeric port"},
		{name: "auth url http", raw: `{"auth_url":"http://app.boxd.sh/api/v1/auth/token"}`, want: "https"},
		{name: "auth url relative", raw: `{"auth_url":"/api/v1/auth/token"}`, want: "absolute URL"},
		{name: "auth url with query", raw: `{"auth_url":"https://app.boxd.sh/token?x=1"}`, want: "query"},
		{name: "unknown field", raw: `{"workspace":"acme"}`, want: "unknown field"},
		{name: "empty allowlist", raw: `{"allowed_snapshots":[]}`, want: "must not be empty"},
		{name: "wildcard mixed", raw: `{"allowed_snapshots":["*","base"]}`, want: "cannot mix wildcard"},
		{name: "blank snapshot", raw: `{"allowed_snapshots":[" "]}`, want: "non-empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseProviderConfig(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBoxdValidatePoolAllowsOmittedOrClassSizedDefaults(t *testing.T) {
	policy := boxdPolicyForTest(t, "", nil)
	if err := (Definition{}).ValidatePool(policy); err != nil {
		t.Fatalf("validate pool without defaults: %v", err)
	}
	policy.DefaultProvisioning.CPU = new(2)
	policy.DefaultProvisioning.MemoryMB = new(8192)
	if err := (Definition{}).ValidatePool(policy); err != nil {
		t.Fatalf("validate pool with 2x8G defaults: %v", err)
	}
	policy.DefaultProvisioning.MemoryMB = nil
	if err := (Definition{}).ValidatePool(policy); err != nil {
		t.Fatalf("validate pool with cpu-only default: %v", err)
	}
	policy.DefaultProvisioning.CPU = nil
	policy.DefaultProvisioning.MemoryMB = new(16384)
	if err := (Definition{}).ValidatePool(policy); err != nil {
		t.Fatalf("validate pool with memory-only default: %v", err)
	}
}

func TestBoxdValidatePoolRejectsUnsupportedSizes(t *testing.T) {
	tests := []struct {
		name     string
		cpu      *int
		memoryMB *int
		want     string
	}{
		{name: "unknown cpu", cpu: new(3), memoryMB: new(8192), want: "cpu=3 memory_mb=8192 is not a supported"},
		{name: "mismatched pair", cpu: new(1), memoryMB: new(8192), want: "cpu=1 memory_mb=8192 is not a supported"},
		{name: "cpu only", cpu: new(8), want: "cpu=8 is not a supported"},
		{name: "memory only", memoryMB: new(2048), want: "memory_mb=2048 is not a supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := boxdPolicyForTest(t, "", nil)
			policy.DefaultProvisioning.CPU = test.cpu
			policy.DefaultProvisioning.MemoryMB = test.memoryMB
			err := (Definition{}).ValidatePool(policy)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBoxdValidatePoolRequiresResourceLimits(t *testing.T) {
	policy := boxdPolicyForTest(t, "", nil)
	policy.ResourceLimits.MaxMachineMemoryMB = nil
	err := (Definition{}).ValidatePool(policy)
	if err == nil || !strings.Contains(err.Error(), "max_machine_memory_mb") {
		t.Fatalf("missing limit error = %v", err)
	}
}

func TestBoxdValidateMachineProvisioningEnforcesSnapshotAllowlist(t *testing.T) {
	policy := boxdPolicyForTest(t, "team-workspace", json.RawMessage(`{"allowed_snapshots":["team-workspace"]}`))
	definition := Definition{}
	if err := definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team-workspace", "")},
	); err != nil {
		t.Fatalf("validate allowed snapshot: %v", err)
	}
	err := definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "other", "")},
	)
	if err == nil || !strings.Contains(err.Error(), "allowed_snapshots") {
		t.Fatalf("disallowed snapshot error = %v", err)
	}
	// The base image is not a snapshot and is always allowed.
	if err := definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "", "")},
	); err != nil {
		t.Fatalf("validate base image: %v", err)
	}
	// Without an allowlist only the pool default snapshot is allowed.
	policy = boxdPolicyForTest(t, "team-workspace", nil)
	err = definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "other", "")},
	)
	if err == nil || !strings.Contains(err.Error(), "allowed_snapshots") {
		t.Fatalf("non-default snapshot error = %v", err)
	}
}

func TestBoxdValidateMachineProvisioningRejectsInvalidOptions(t *testing.T) {
	policy := boxdPolicyForTest(t, "", nil)
	tests := []struct {
		name    string
		options map[string]json.RawMessage
		want    string
	}{
		{
			name:    "unknown option",
			options: map[string]json.RawMessage{"image": json.RawMessage(`"ubuntu"`)},
			want:    `unknown field "image"`,
		},
		{
			name:    "wildcard snapshot",
			options: map[string]json.RawMessage{"snapshot": json.RawMessage(`"*"`)},
			want:    `must not be "*"`,
		},
		{
			name:    "snapshot with spaces",
			options: map[string]json.RawMessage{"snapshot": json.RawMessage(`"my snapshot"`)},
			want:    "snapshot: value is invalid",
		},
		{
			name:    "sleep window not a number",
			options: map[string]json.RawMessage{"sleep_after_ms": json.RawMessage(`"30000"`)},
			want:    "decode boxd provider_options",
		},
		{
			name:    "startup script not a string",
			options: map[string]json.RawMessage{"startup_script": json.RawMessage(`["echo"]`)},
			want:    "startup_script",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (Definition{}).ValidateMachineProvisioning(
				policy,
				executionstore.MachineProvisioningConfig{ProviderOptions: test.options},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
	if err := (Definition{}).ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{},
	); err == nil || !strings.Contains(err.Error(), "requires provider_options") {
		t.Fatalf("missing options error = %v", err)
	}
}

func TestBoxdValidateMachineProvisioningChecksSleepWindow(t *testing.T) {
	policy := boxdPolicyForTest(t, "", nil)
	definition := Definition{}
	if err := definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testSleepOptions(t, 30_000)},
	); err != nil {
		t.Fatalf("validate minimum sleep window: %v", err)
	}
	if err := definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testSleepOptions(t, 0)},
	); err != nil {
		t.Fatalf("validate disabled sleep window: %v", err)
	}
	err := definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testSleepOptions(t, 29_000)},
	)
	if err == nil || !strings.Contains(err.Error(), "sleep_after_ms") {
		t.Fatalf("sub-minimum sleep window error = %v", err)
	}
}

func TestBoxdValidateMachineProvisioningChecksSuspendWindow(t *testing.T) {
	policy := boxdPolicyForTest(t, "", nil)
	definition := Definition{}
	for _, valid := range []int{minimumAutoSuspendSecs, 300} {
		if err := definition.ValidateMachineProvisioning(
			policy,
			executionstore.MachineProvisioningConfig{
				ProviderOptions: testSleepOptionsWithWindow(t, 30_000, valid),
			},
		); err != nil {
			t.Fatalf("validate auto_suspend_secs %d: %v", valid, err)
		}
	}
	tests := []struct {
		name           string
		sleepAfterMS   int
		autoSuspendSec int
		want           string
	}{
		{
			name:           "below the daemon heartbeat margin",
			sleepAfterMS:   30_000,
			autoSuspendSec: minimumAutoSuspendSecs - 1,
			want:           "auto_suspend_secs must be between",
		},
		{
			name:           "without a sleep window",
			sleepAfterMS:   0,
			autoSuspendSec: 60,
			want:           "auto_suspend_secs requires sleep_after_ms",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := definition.ValidateMachineProvisioning(
				policy,
				executionstore.MachineProvisioningConfig{
					ProviderOptions: testSleepOptionsWithWindow(t, test.sleepAfterMS, test.autoSuspendSec),
				},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBoxdAutoSuspendWindowDefaultsOnlyWithASleepWindow(t *testing.T) {
	for _, test := range []struct {
		name    string
		options providerOptions
		want    uint32
	}{
		{name: "no sleep window", options: providerOptions{}, want: 0},
		{name: "sleep window defaults", options: providerOptions{SleepAfterMS: 30_000}, want: defaultAutoSuspendSecs},
		{
			name:    "tuned window",
			options: providerOptions{SleepAfterMS: 30_000, AutoSuspendSecs: 900},
			want:    900,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.options.autoSuspendSecs(); got != test.want {
				t.Fatalf("auto suspend window = %d, want %d", got, test.want)
			}
		})
	}
}

func TestBoxdBuildIntentKeepsConfiguredSize(t *testing.T) {
	policy := boxdPolicyForTest(t, "", nil)
	intent, err := (Definition{}).BuildMachineProvisioningIntent(
		policy,
		executionstore.MachineProvisioningConfig{
			CPU:             new(4),
			MemoryMB:        new(16384),
			ProviderOptions: testOptions(t, "", ""),
		},
	)
	if err != nil {
		t.Fatalf("build intent: %v", err)
	}
	if intent.CPU == nil || *intent.CPU != 4 || intent.MemoryMB == nil || *intent.MemoryMB != 16384 {
		t.Fatalf("intent resources = cpu %v memory %v, want configured size", intent.CPU, intent.MemoryMB)
	}
	intent, err = (Definition{}).BuildMachineProvisioningIntent(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "", "")},
	)
	if err != nil {
		t.Fatalf("build unsized intent: %v", err)
	}
	if intent.CPU != nil || intent.MemoryMB != nil {
		t.Fatalf("unsized intent resources = cpu %v memory %v, want unresolved", intent.CPU, intent.MemoryMB)
	}
}

func TestBoxdPrepareProvisioningResolvesSize(t *testing.T) {
	t.Run("configured pair", func(t *testing.T) {
		api := newFakeAPI()
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			testMachineProvisioning(t, "", ""),
		)
		if err != nil || *facts.CPU != 2 || *facts.MemoryMB != 8192 {
			t.Fatalf("facts = %+v, error %v", facts, err)
		}
	})
	t.Run("cpu names the class", func(t *testing.T) {
		facts, err := newTestProvider(newFakeAPI()).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{CPU: new(4), ProviderOptions: testOptions(t, "", "")},
		)
		if err != nil || *facts.CPU != 4 || *facts.MemoryMB != 16384 {
			t.Fatalf("facts = %+v, error %v", facts, err)
		}
	})
	t.Run("memory names the class", func(t *testing.T) {
		facts, err := newTestProvider(newFakeAPI()).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{MemoryMB: new(4096), ProviderOptions: testOptions(t, "", "")},
		)
		if err != nil || *facts.CPU != 1 || *facts.MemoryMB != 4096 {
			t.Fatalf("facts = %+v, error %v", facts, err)
		}
	})
	t.Run("unsupported class", func(t *testing.T) {
		_, err := newTestProvider(newFakeAPI()).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{CPU: new(3), ProviderOptions: testOptions(t, "", "")},
		)
		if err == nil || !strings.Contains(err.Error(), "not a supported boxd size class") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("snapshot size", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotFound = true
		api.snapshot = snapshotInfo{Name: "team-workspace", Status: "ready", VCPU: 4, MemoryBytes: 16384 * mebibyte}
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team-workspace", "")},
		)
		if err != nil || *facts.CPU != 4 || *facts.MemoryMB != 16384 || api.snapshotName != "team-workspace" {
			t.Fatalf("facts = %+v, error %v, lookup %q", facts, err, api.snapshotName)
		}
	})
	t.Run("snapshot size agrees with a configured size", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotFound = true
		api.snapshot = snapshotInfo{Name: "team-workspace", Status: "ready", VCPU: 2, MemoryBytes: 8192 * mebibyte}
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{
				CPU:             new(2),
				ProviderOptions: testOptions(t, "team-workspace", ""),
			},
		)
		if err != nil || *facts.CPU != 2 || *facts.MemoryMB != 8192 {
			t.Fatalf("facts = %+v, error %v", facts, err)
		}
	})
	t.Run("snapshot size conflicts with a configured size", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotFound = true
		api.snapshot = snapshotInfo{Name: "team-workspace", Status: "ready", VCPU: 4, MemoryBytes: 16384 * mebibyte}
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{
				CPU:             new(1),
				MemoryMB:        new(4096),
				ProviderOptions: testOptions(t, "team-workspace", ""),
			},
		)
		if err == nil || !strings.Contains(err.Error(), "does not match snapshot") ||
			!strings.Contains(err.Error(), "restores at cpu=4 memory_mb=16384") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("snapshot not ready", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotFound = true
		api.snapshot = snapshotInfo{Name: "team-workspace", Status: "pending", VCPU: 1, MemoryBytes: 4096 * mebibyte}
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team-workspace", "")},
		)
		if err == nil || !strings.Contains(err.Error(), "is not ready") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("snapshot missing", func(t *testing.T) {
		_, err := newTestProvider(newFakeAPI()).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team-workspace", "")},
		)
		if err == nil || !strings.Contains(err.Error(), "was not found") ||
			errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("snapshot lookup unavailable", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotErr = apiError{Code: codes.Unavailable}
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team-workspace", "")},
		)
		if !errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
			t.Fatalf("error = %v, want provider unavailable", err)
		}
	})
	t.Run("snapshot lookup denied", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotErr = apiError{Code: codes.PermissionDenied}
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team-workspace", "")},
		)
		if err == nil || errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
			t.Fatalf("error = %v, want permanent failure", err)
		}
	})
	t.Run("org default", func(t *testing.T) {
		api := newFakeAPI()
		api.orgDefaults = machineSize{VCPU: 2, MemoryBytes: 8192 * mebibyte}
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "", "")},
		)
		if err != nil || *facts.CPU != 2 || *facts.MemoryMB != 8192 {
			t.Fatalf("facts = %+v, error %v", facts, err)
		}
	})
	t.Run("org default unavailable", func(t *testing.T) {
		api := newFakeAPI()
		api.orgDefaultsErr = apiError{Code: codes.ResourceExhausted}
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "", "")},
		)
		if !errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
			t.Fatalf("error = %v, want provider unavailable", err)
		}
	})
	t.Run("org default unusable", func(t *testing.T) {
		api := newFakeAPI()
		api.orgDefaults = machineSize{VCPU: 2, MemoryBytes: 1000}
		_, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "", "")},
		)
		if err == nil || !strings.Contains(err.Error(), "unusable memory size") {
			t.Fatalf("error = %v", err)
		}
	})
}
