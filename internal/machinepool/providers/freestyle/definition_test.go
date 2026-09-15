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
		json.RawMessage(`{"api_base_url":"https://freestyle.example/api/"}`),
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
	if !ok || client.apiBaseURL != "https://freestyle.example/api" || client.apiToken != "token" {
		t.Fatalf("REST client = %#v", provider.api)
	}
	if provider.omnaraAPIURL != "https://api.omnara.test/v1" {
		t.Fatalf("Omnara API URL = %q", provider.omnaraAPIURL)
	}
}

func TestDefinitionRequiresTokenAndHTTPSBaseURL(t *testing.T) {
	_, err := (Definition{}).NewProvider(nil, providers.RuntimeConfig{})
	if err == nil || !strings.Contains(err.Error(), "auth token is required") {
		t.Fatalf("missing token error = %v", err)
	}
	_, err = (Definition{}).NewProvider(
		json.RawMessage(`{"api_base_url":"http://freestyle.example"}`),
		providers.RuntimeConfig{ProviderAuthToken: "token"},
	)
	if err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("insecure base URL error = %v", err)
	}
}

func TestDefinitionValidatesSnapshotAllowlistAndIdleTimeout(t *testing.T) {
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
	provisioning.ProviderOptions["idle_timeout_seconds"] = rawJSON(t, 31536001)
	if err := (Definition{}).ValidateMachineProvisioning(policy, provisioning); err == nil ||
		!strings.Contains(err.Error(), "between 1 and 31536000") {
		t.Fatalf("idle timeout error = %v", err)
	}
	provisioning.ProviderOptions["idle_timeout_seconds"] = rawJSON(t, 0)
	if err := (Definition{}).ValidateMachineProvisioning(policy, provisioning); err == nil ||
		!strings.Contains(err.Error(), "between 1 and 31536000") {
		t.Fatalf("zero idle timeout error = %v", err)
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
