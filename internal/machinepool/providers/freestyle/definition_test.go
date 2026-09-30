package freestyle

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestDefinitionCreatesProvider(t *testing.T) {
	runtime, err := (Definition{}).NewProvider(
		nil,
		providers.RuntimeConfig{
			OmnaraAPIURL:      "https://api.omnara.test/v1",
			ProviderAuthToken: "token",
		},
	)
	if err != nil {
		t.Fatalf("create freestyle provider: %v", err)
	}
	provider, ok := runtime.(*provider)
	if !ok {
		t.Fatalf("provider type = %T, want *provider", runtime)
	}
	client, ok := provider.api.(*restClient)
	if !ok || client.apiBaseURL != apiBaseURL || client.apiToken != "token" {
		t.Fatalf("REST client = %#v", provider.api)
	}
	if provider.omnaraAPIURL != "https://api.omnara.test/v1" {
		t.Fatalf("Omnara API URL = %q", provider.omnaraAPIURL)
	}
}

func TestDefinitionRequiresToken(t *testing.T) {
	_, err := (Definition{}).NewProvider(nil, providers.RuntimeConfig{})
	if err == nil || !strings.Contains(err.Error(), "auth token is required") {
		t.Fatalf("missing token error = %v", err)
	}
}

func TestDefinitionValidatesSnapshotAllowlistAndSleepAfter(t *testing.T) {
	policy := validPolicy(t)
	policy.ProviderConfig = json.RawMessage(`{"allowed_snapshots":["ubuntu-24.04"]}`)
	if err := (Definition{}).ValidateMachineProvisioning(policy, testProvisioning(t)); err != nil {
		t.Fatalf("validate freestyle provisioning: %v", err)
	}

	provisioning := testProvisioning(t)
	provisioning.ProviderOptions["snapshot"] = rawJSON(t, "other-snapshot")
	if err := (Definition{}).ValidateMachineProvisioning(policy, provisioning); err == nil ||
		!strings.Contains(err.Error(), "allowed_snapshots") {
		t.Fatalf("disallowed snapshot error = %v", err)
	}

	provisioning = testProvisioning(t)
	provisioning.ProviderOptions["sleep_after_ms"] = rawJSON(t, 29999)
	if err := (Definition{}).ValidateMachineProvisioning(policy, provisioning); err == nil ||
		!strings.Contains(err.Error(), "sleep_after_ms must be at least 30000") {
		t.Fatalf("short sleep_after_ms error = %v", err)
	}
	provisioning.ProviderOptions["sleep_after_ms"] = rawJSON(t, 30000)
	if err := (Definition{}).ValidateMachineProvisioning(policy, provisioning); err != nil {
		t.Fatalf("validate sleep_after_ms: %v", err)
	}
}

func TestDefinitionRejectsUnknownProviderOptions(t *testing.T) {
	provisioning := testProvisioning(t)
	provisioning.ProviderOptions["region"] = rawJSON(t, "us-west")
	err := (Definition{}).ValidateMachineProvisioning(validPolicy(t), provisioning)
	if err == nil || !strings.Contains(err.Error(), `unknown field "region"`) {
		t.Fatalf("unknown provider option error = %v", err)
	}
}

func validPolicy(t *testing.T) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 16384
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: testProvisioning(t),
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
	}
}
