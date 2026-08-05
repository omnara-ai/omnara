package executionstore

import (
	"encoding/json"
	"maps"
	"testing"
)

func testMachineProvisioning(
	t *testing.T,
	cpu int,
	memoryMB int,
	providerOptions map[string]any,
) MachineProvisioningConfig {
	t.Helper()
	rawProviderOptions := make(map[string]json.RawMessage, len(providerOptions))
	for key, value := range providerOptions {
		rawProviderOptions[key] = mustTestRawJSON(t, value)
	}
	return MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: rawProviderOptions,
	}
}

func testMachineProvisioningOverlay(
	t *testing.T,
	cpu *int,
	memoryMB *int,
	providerOptions map[string]any,
) MachineProvisioningOverlay {
	t.Helper()
	var rawProviderOptions map[string]json.RawMessage
	if providerOptions != nil {
		rawProviderOptions = make(map[string]json.RawMessage, len(providerOptions))
		for key, value := range providerOptions {
			rawProviderOptions[key] = mustTestRawJSON(t, value)
		}
	}
	return MachineProvisioningOverlay{
		CPU:             cpu,
		MemoryMB:        memoryMB,
		ProviderOptions: rawProviderOptions,
	}
}

func requireMachineProvisioningForTest(
	t *testing.T,
	got MachineProvisioningConfig,
	want MachineProvisioningConfig,
) {
	t.Helper()
	if !sameIntPtr(got.CPU, want.CPU) {
		t.Fatalf("machine provisioning cpu = %v, want %v", got.CPU, want.CPU)
	}
	if !sameIntPtr(got.MemoryMB, want.MemoryMB) {
		t.Fatalf("machine provisioning memory_mb = %v, want %v", got.MemoryMB, want.MemoryMB)
	}
	if len(got.ProviderOptions) != len(want.ProviderOptions) {
		t.Fatalf("machine provisioning provider_options = %+v, want %+v", got.ProviderOptions, want.ProviderOptions)
	}
	for key, wantValue := range want.ProviderOptions {
		if !sameJSON(got.ProviderOptions[key], wantValue) {
			t.Fatalf(
				"machine provisioning provider_options[%s] = %s, want %s",
				key,
				got.ProviderOptions[key],
				wantValue,
			)
		}
	}
}

func requireMachineEnvironmentForTest(t *testing.T, got, want MachineEnvironment) {
	t.Helper()
	if !maps.Equal(got.Env, want.Env) {
		t.Fatalf("machine environment env = %+v, want %+v", got.Env, want.Env)
	}
	if !maps.Equal(got.SecretEnv, want.SecretEnv) {
		t.Fatalf("machine environment secret_env = %+v, want %+v", got.SecretEnv, want.SecretEnv)
	}
}

func TestMachineProvisioningFromRecordAllowsUnresolvedProviderResources(t *testing.T) {
	provisioning, err := MachineProvisioningFromRecord(MachineRecord{
		ProviderOptions: json.RawMessage(`{"snapshot":"team"}`),
	})
	if err != nil {
		t.Fatalf("read unresolved machine provisioning: %v", err)
	}
	if provisioning.CPU != nil || provisioning.MemoryMB != nil {
		t.Fatalf("unresolved resources = cpu %v memory %v", provisioning.CPU, provisioning.MemoryMB)
	}
}

func ptrForMachineTest[T any](value T) *T {
	return &value
}

func mustTestRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}
