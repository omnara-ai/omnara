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
	if client.authURL != authURL || client.apiKey != "bxd_token" {
		t.Fatalf("client config = auth %q key %q", client.authURL, client.apiKey)
	}
	if concrete.omnaraAPIURL != "https://api.omnara.test/v1" {
		t.Fatalf("omnara API URL = %q", concrete.omnaraAPIURL)
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
	if config.AllowedSnapshots != nil {
		t.Fatalf("default boxd provider config = %+v", config)
	}
}

func TestParseBoxdProviderConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unknown field", raw: `{"api_url":"boxd.example:9443"}`, want: "unknown field"},
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
	if err := definition.ValidateMachineProvisioning(
		policy,
		executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "", "")},
	); err != nil {
		t.Fatalf("validate base image: %v", err)
	}
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
			name:    "empty snapshot",
			options: map[string]json.RawMessage{"snapshot": json.RawMessage(`""`)},
			want:    "omit it to use the base image",
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

func TestBoxdBuildIntentLeavesResourcesUnresolved(t *testing.T) {
	intent, err := (Definition{}).BuildMachineProvisioningIntent(
		boxdPolicyForTest(t, "", nil),
		executionstore.MachineProvisioningConfig{
			CPU:             new(4),
			MemoryMB:        new(16384),
			ProviderOptions: testOptions(t, "", ""),
		},
	)
	if err != nil {
		t.Fatalf("build intent: %v", err)
	}
	if intent.CPU != nil || intent.MemoryMB != nil {
		t.Fatalf("intent resources = cpu %v memory %v, want unresolved", intent.CPU, intent.MemoryMB)
	}
}

func TestBoxdPrepareProvisioningResolvesSize(t *testing.T) {
	t.Run("stored facts", func(t *testing.T) {
		api := newFakeAPI()
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			testMachineProvisioning(t, "team-workspace", ""),
		)
		if err != nil || *facts.CPU != 2 || *facts.MemoryMB != 8192 || api.snapshotName != "" {
			t.Fatalf("facts = %+v, error %v, snapshot lookup %q", facts, err, api.snapshotName)
		}
	})

	t.Run("snapshot size", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotFound = true
		api.snapshot = snapshotInfo{Status: "ready", VCPU: 4, MemoryBytes: 16384 * mebibyte}
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "team-workspace", "")},
		)
		if err != nil || *facts.CPU != 4 || *facts.MemoryMB != 16384 || api.snapshotName != "team-workspace" {
			t.Fatalf("facts = %+v, error %v, lookup %q", facts, err, api.snapshotName)
		}
	})

	t.Run("snapshot not ready", func(t *testing.T) {
		api := newFakeAPI()
		api.snapshotFound = true
		api.snapshot = snapshotInfo{Status: "pending", VCPU: 1, MemoryBytes: 4096 * mebibyte}
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
