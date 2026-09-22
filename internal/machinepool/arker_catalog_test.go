package machinepool

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// The catalog rejects a protected pool whose provider cannot observe runtime
// state, so this is what proves arker is usable with runtime protection rather
// than only in the unprotected case.
func TestDefaultCatalogAcceptsAProtectedArkerPool(t *testing.T) {
	policy := executionstore.MachinePoolProviderPolicy{
		RuntimeProtectionEnabled: true,
		ProviderConfig: json.RawMessage(
			`{"allowed_sources":["ubuntu-base"],"allowed_providers":["aws"],"allowed_regions":["us-west-2"]}`,
		),
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        intPtr(64),
			MaxTotalMemoryMB:   intPtr(131072),
			MinMachineCPU:      intPtr(1),
			MinMachineMemoryMB: intPtr(512),
			MaxMachineCPU:      intPtr(8),
			MaxMachineMemoryMB: intPtr(16384),
		},
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			CPU:      intPtr(2),
			MemoryMB: intPtr(4096),
			ProviderOptions: map[string]json.RawMessage{
				"source":         json.RawMessage(`"ubuntu-base"`),
				"provider":       json.RawMessage(`"aws"`),
				"region":         json.RawMessage(`"us-west-2"`),
				"startup_script": json.RawMessage(`""`),
			},
		},
	}
	if err := DefaultCatalog().ValidatePool("arker", policy); err != nil {
		t.Fatalf("validate protected arker pool: %v", err)
	}
}

func intPtr(value int) *int { return &value }
