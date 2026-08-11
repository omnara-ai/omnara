package executionstore

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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

func TestCheckProvisioningResourceAdmissionEnforcesMinimums(t *testing.T) {
	err := checkProvisioningResourceAdmission(
		0,
		0,
		MachinePoolResources{},
		MachinePoolResources{CPU: 1, MemoryMB: 1024},
		MachineResourceLimits{
			MinMachineCPU:      intPtrForMachinePoolStoreTest(2),
			MinMachineMemoryMB: intPtrForMachinePoolStoreTest(2048),
		},
	)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("provisioning minimum error = %v, want state transition conflict", err)
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

func TestValidateMachineResourcesWithinPerMachineLimitsEnforcesMinimums(t *testing.T) {
	for _, test := range []struct {
		name      string
		resources MachinePoolResources
		limits    MachineResourceLimits
		want      string
	}{
		{
			name:      "cpu",
			resources: MachinePoolResources{CPU: 1, MemoryMB: 2048},
			limits:    MachineResourceLimits{MinMachineCPU: intPtrForMachinePoolStoreTest(2)},
			want:      "cpu is below min_machine_cpu",
		},
		{
			name:      "memory",
			resources: MachinePoolResources{CPU: 2, MemoryMB: 1024},
			limits:    MachineResourceLimits{MinMachineMemoryMB: intPtrForMachinePoolStoreTest(2048)},
			want:      "memory_mb is below min_machine_memory_mb",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateMachineResourcesWithinPerMachineLimits(test.resources, test.limits)
			if err == nil || err.Error() != test.want {
				t.Fatalf("minimum validation error = %v, want %q", err, test.want)
			}
		})
	}

	if err := validateMachineResourcesWithinPerMachineLimits(
		MachinePoolResources{},
		MachineResourceLimits{
			MinMachineCPU:      intPtrForMachinePoolStoreTest(2),
			MinMachineMemoryMB: intPtrForMachinePoolStoreTest(2048),
		},
	); err != nil {
		t.Fatalf("unresolved resources rejected before provider resolution: %v", err)
	}
}

func TestValidateProjectMachinePoolGrantStaticPolicyMinimums(t *testing.T) {
	maxMachineCPU := 8
	for _, test := range []struct {
		name   string
		pool   MachinePoolRecord
		config projectMachinePoolGrantConfig
		want   string
	}{
		{
			name:   "below pool minimum",
			pool:   MachinePoolRecord{MinMachineCPU: intPtrForMachinePoolStoreTest(4), MaxMachineCPU: &maxMachineCPU},
			config: projectMachinePoolGrantConfig{MinMachineCPU: intPtrForMachinePoolStoreTest(2)},
			want:   "pool grant min_machine_cpu cannot be lower than machine pool min_machine_cpu",
		},
		{
			name:   "without pool maximum",
			config: projectMachinePoolGrantConfig{MinMachineCPU: intPtrForMachinePoolStoreTest(2)},
			want:   "pool grant min_machine_cpu is not supported by the machine pool",
		},
		{
			name:   "above pool maximum",
			pool:   MachinePoolRecord{MaxMachineCPU: &maxMachineCPU},
			config: projectMachinePoolGrantConfig{MinMachineCPU: intPtrForMachinePoolStoreTest(9)},
			want:   "pool grant min_machine_cpu cannot exceed max_machine_cpu",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateProjectMachinePoolGrantStaticPolicy(test.config, test.pool)
			if err == nil || err.Error() != test.want {
				t.Fatalf("static minimum policy error = %v, want %q", err, test.want)
			}
		})
	}
}

func intPtrForMachinePoolStoreTest(value int) *int {
	return &value
}
