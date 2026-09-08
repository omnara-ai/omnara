package executionstore

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
	if diff := cmp.Diff(want.CPU, got.CPU); diff != "" {
		t.Fatalf("machine provisioning cpu (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(want.MemoryMB, got.MemoryMB); diff != "" {
		t.Fatalf("machine provisioning memory_mb (-want +got):\n%s", diff)
	}
	if len(got.ProviderOptions) != len(want.ProviderOptions) {
		t.Fatalf(
			"machine provisioning provider_options keys = %v, want %v",
			slices.Sorted(maps.Keys(got.ProviderOptions)), slices.Sorted(maps.Keys(want.ProviderOptions)),
		)
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
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("machine environment (-want +got):\n%s", diff)
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
