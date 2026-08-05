package providers

import (
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestProviderMachinePoolResourceContracts(t *testing.T) {
	for _, provider := range []string{"unikraft", "daytona", "blaxel"} {
		t.Run(provider+" valid", func(t *testing.T) {
			policy := validProviderResourcePolicyForTest(provider)
			if err := ValidateMachinePoolResourcePolicy(provider, policy, resourcePolicyForTest(provider)); err != nil {
				t.Fatalf("validate %s resource policy: %v", provider, err)
			}
		})
	}
	t.Run("daytona valid with optional defaults", func(t *testing.T) {
		policy := validProviderResourcePolicyForTest("daytona")
		policy.DefaultProvisioning.CPU = resourceIntPtr(1)
		policy.DefaultProvisioning.MemoryMB = resourceIntPtr(1024)
		if err := ValidateMachinePoolResourcePolicy(
			"daytona",
			policy,
			resourcePolicyForTest("daytona"),
		); err != nil {
			t.Fatalf("validate daytona resource policy with defaults: %v", err)
		}
	})
	tests := []struct {
		name     string
		provider string
		mutate   func(*executionstore.MachinePoolProviderPolicy)
		want     string
	}{
		{
			name:     "unikraft requires default cpu",
			provider: "unikraft",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.DefaultProvisioning.CPU = nil },
			want:     "require default_machine_cpu",
		},
		{
			name:     "unikraft requires default memory",
			provider: "unikraft",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.DefaultProvisioning.MemoryMB = nil },
			want:     "require default_machine_memory_mb",
		},
		{
			name:     "unikraft requires total cpu limit",
			provider: "unikraft",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxTotalCPU = nil },
			want:     "require max_total_cpu",
		},
		{
			name:     "unikraft requires total memory limit",
			provider: "unikraft",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxTotalMemoryMB = nil },
			want:     "require max_total_memory_mb",
		},
		{
			name:     "unikraft requires per-machine cpu limit",
			provider: "unikraft",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxMachineCPU = nil },
			want:     "require max_machine_cpu",
		},
		{
			name:     "unikraft requires per-machine memory limit",
			provider: "unikraft",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxMachineMemoryMB = nil },
			want:     "require max_machine_memory_mb",
		},
		{
			name:     "daytona requires total cpu limit",
			provider: "daytona",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxTotalCPU = nil },
			want:     "require max_total_cpu",
		},
		{
			name:     "daytona requires total memory limit",
			provider: "daytona",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxTotalMemoryMB = nil },
			want:     "require max_total_memory_mb",
		},
		{
			name:     "daytona requires per-machine cpu limit",
			provider: "daytona",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxMachineCPU = nil },
			want:     "require max_machine_cpu",
		},
		{
			name:     "daytona requires per-machine memory limit",
			provider: "daytona",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxMachineMemoryMB = nil },
			want:     "require max_machine_memory_mb",
		},
		{
			name:     "blaxel forbids default cpu",
			provider: "blaxel",
			mutate: func(policy *executionstore.MachinePoolProviderPolicy) {
				policy.DefaultProvisioning.CPU = resourceIntPtr(1)
			},
			want: "do not support default_machine_cpu",
		},
		{
			name:     "blaxel forbids total cpu limit",
			provider: "blaxel",
			mutate: func(policy *executionstore.MachinePoolProviderPolicy) {
				policy.ResourceLimits.MaxTotalCPU = resourceIntPtr(4)
			},
			want: "do not support max_total_cpu",
		},
		{
			name:     "blaxel forbids per-machine cpu limit",
			provider: "blaxel",
			mutate: func(policy *executionstore.MachinePoolProviderPolicy) {
				policy.ResourceLimits.MaxMachineCPU = resourceIntPtr(2)
			},
			want: "do not support max_machine_cpu",
		},
		{
			name:     "blaxel requires default memory",
			provider: "blaxel",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.DefaultProvisioning.MemoryMB = nil },
			want:     "require default_machine_memory_mb",
		},
		{
			name:     "blaxel requires total memory limit",
			provider: "blaxel",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxTotalMemoryMB = nil },
			want:     "require max_total_memory_mb",
		},
		{
			name:     "blaxel requires per-machine memory limit",
			provider: "blaxel",
			mutate:   func(policy *executionstore.MachinePoolProviderPolicy) { policy.ResourceLimits.MaxMachineMemoryMB = nil },
			want:     "require max_machine_memory_mb",
		},
		{
			name:     "unikraft rejects negative total cpu limit",
			provider: "unikraft",
			mutate: func(policy *executionstore.MachinePoolProviderPolicy) {
				policy.ResourceLimits.MaxTotalCPU = resourceIntPtr(-1)
			},
			want: "max_total_cpu cannot be negative",
		},
		{
			name:     "blaxel rejects negative total memory limit",
			provider: "blaxel",
			mutate: func(policy *executionstore.MachinePoolProviderPolicy) {
				policy.ResourceLimits.MaxTotalMemoryMB = resourceIntPtr(-1)
			},
			want: "max_total_memory_mb cannot be negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := validProviderResourcePolicyForTest(test.provider)
			test.mutate(&policy)
			err := ValidateMachinePoolResourcePolicy(
				test.provider,
				policy,
				resourcePolicyForTest(test.provider),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate resource policy error = %v, want %q", err, test.want)
			}
		})
	}
}

func validProviderResourcePolicyForTest(
	provider string,
) executionstore.MachinePoolProviderPolicy {
	maxCPU := 8
	maxMemoryMB := 8192
	policy := executionstore.MachinePoolProviderPolicy{
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
	}
	switch provider {
	case "unikraft":
		policy.DefaultProvisioning.CPU = resourceIntPtr(1)
		policy.DefaultProvisioning.MemoryMB = resourceIntPtr(1024)
	case "daytona":
	case "blaxel":
		policy.DefaultProvisioning.MemoryMB = resourceIntPtr(1024)
		policy.ResourceLimits.MaxTotalCPU = nil
		policy.ResourceLimits.MaxMachineCPU = nil
	}
	return policy
}

func resourcePolicyForTest(provider string) MachineResourcePolicy {
	switch provider {
	case "unikraft":
		return MachineResourcePolicy{
			CPU: MachineResourceContract{
				PoolDefault:  MachineResourceRequired,
				Limits:       MachineResourceRequired,
				Provisioning: MachineResourceConfigured,
			},
			MemoryMB: MachineResourceContract{
				PoolDefault:  MachineResourceRequired,
				Limits:       MachineResourceRequired,
				Provisioning: MachineResourceConfigured,
			},
		}
	case "daytona":
		return MachineResourcePolicy{
			CPU: MachineResourceContract{
				PoolDefault:  MachineResourceOptional,
				Limits:       MachineResourceRequired,
				Provisioning: MachineResourceProviderResolved,
			},
			MemoryMB: MachineResourceContract{
				PoolDefault:  MachineResourceOptional,
				Limits:       MachineResourceRequired,
				Provisioning: MachineResourceProviderResolved,
			},
		}
	case "blaxel":
		return MachineResourcePolicy{
			CPU: MachineResourceContract{
				PoolDefault:  MachineResourceUnsupported,
				Limits:       MachineResourceUnsupported,
				Provisioning: MachineResourceUnmodeled,
			},
			MemoryMB: MachineResourceContract{
				PoolDefault:  MachineResourceRequired,
				Limits:       MachineResourceRequired,
				Provisioning: MachineResourceConfigured,
			},
		}
	default:
		panic("unsupported test provider " + provider)
	}
}

func resourceIntPtr(value int) *int {
	return &value
}
