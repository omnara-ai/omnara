package executionstore

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/management"
)

func TestPrepareMachinePoolConfigInputAllowsIndependentResourceDefaultsAndCaps(t *testing.T) {
	value := 1
	for _, test := range []struct {
		name  string
		apply func(*CreateMachinePoolInput)
	}{
		{name: "no resource policy", apply: func(*CreateMachinePoolInput) {}},
		{name: "cpu default only", apply: func(input *CreateMachinePoolInput) { input.DefaultMachineCPU = &value }},
		{name: "memory default only", apply: func(input *CreateMachinePoolInput) { input.DefaultMachineMemoryMB = &value }},
		{name: "cpu total cap only", apply: func(input *CreateMachinePoolInput) { input.MaxTotalCPU = &value }},
		{name: "memory total cap only", apply: func(input *CreateMachinePoolInput) { input.MaxTotalMemoryMB = &value }},
		{name: "cpu machine cap only", apply: func(input *CreateMachinePoolInput) { input.MaxMachineCPU = &value }},
		{name: "memory machine cap only", apply: func(input *CreateMachinePoolInput) { input.MaxMachineMemoryMB = &value }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := CreateMachinePoolInput{
				ManagementKind:                management.Cluster,
				ProviderAuthEnvVar:            "TEST_PROVIDER_TOKEN",
				DefaultMachineProviderOptions: json.RawMessage(`{}`),
				MaxTotalMachines:              1,
			}
			test.apply(&input)
			if _, err := prepareMachinePoolConfigInput(&input); err != nil {
				t.Fatalf("prepare independent machine pool resources: %v", err)
			}
		})
	}
}

func TestValidateMachineResourcesWithinPerMachineLimitsIgnoresAggregateLimits(t *testing.T) {
	maxTotalCPU := 0
	maxTotalMemoryMB := 64
	maxMachineMemoryMB := 128
	err := validateMachineResourcesWithinPerMachineLimits(
		MachinePoolResources{CPU: 1, MemoryMB: 128},
		MachineResourceLimits{
			MaxTotalCPU:        &maxTotalCPU,
			MaxTotalMemoryMB:   &maxTotalMemoryMB,
			MaxMachineMemoryMB: &maxMachineMemoryMB,
		},
	)
	if err != nil {
		t.Fatalf("total caps influenced machine shape validation: %v", err)
	}
}
