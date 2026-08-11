package providers

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type MachineResourceFieldMode uint8

const (
	MachineResourceRequired MachineResourceFieldMode = iota
	MachineResourceOptional
	MachineResourceUnsupported
)

type MachineResourceProvisioningMode uint8

const (
	MachineResourceConfigured MachineResourceProvisioningMode = iota
	MachineResourceProviderResolved
	MachineResourceUnmodeled
)

type MachineResourceContract struct {
	PoolDefault  MachineResourceFieldMode
	Limits       MachineResourceFieldMode
	Provisioning MachineResourceProvisioningMode
}

type MachineResourcePolicy struct {
	CPU      MachineResourceContract
	MemoryMB MachineResourceContract
}

func ValidateMachinePoolResourcePolicy(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
	resources MachineResourcePolicy,
) error {
	if err := validatePoolResourcePolicy(
		provider,
		"cpu",
		policy.DefaultProvisioning.CPU,
		policy.ResourceLimits.MaxTotalCPU,
		policy.ResourceLimits.MinMachineCPU,
		policy.ResourceLimits.MaxMachineCPU,
		resources.CPU.PoolDefault,
		resources.CPU.Limits,
	); err != nil {
		return err
	}
	return validatePoolResourcePolicy(
		provider,
		"memory_mb",
		policy.DefaultProvisioning.MemoryMB,
		policy.ResourceLimits.MaxTotalMemoryMB,
		policy.ResourceLimits.MinMachineMemoryMB,
		policy.ResourceLimits.MaxMachineMemoryMB,
		resources.MemoryMB.PoolDefault,
		resources.MemoryMB.Limits,
	)
}

func validatePoolResourcePolicy(
	provider, resource string,
	poolDefault, maxTotal *int,
	minMachine *int,
	maxMachine *int,
	defaultMode, limitsMode MachineResourceFieldMode,
) error {
	defaultField := "default_machine_" + resource
	maxTotalField := "max_total_" + resource
	minMachineField := "min_machine_" + resource
	maxMachineField := "max_machine_" + resource
	if err := validateResourceField(provider, defaultField, poolDefault, defaultMode); err != nil {
		return err
	}
	if err := validateResourceField(provider, maxTotalField, maxTotal, limitsMode); err != nil {
		return err
	}
	if err := validateResourceField(provider, maxMachineField, maxMachine, limitsMode); err != nil {
		return err
	}
	if minMachine != nil && *minMachine < 0 {
		return fmt.Errorf("%s %s cannot be negative", provider, minMachineField)
	}
	if minMachine != nil && limitsMode == MachineResourceUnsupported {
		return fmt.Errorf("%s machine pools do not support %s", provider, minMachineField)
	}
	if poolDefault != nil && *poolDefault <= 0 {
		return fmt.Errorf("%s %s must be positive", provider, defaultField)
	}
	if maxTotal != nil && *maxTotal < 0 {
		return fmt.Errorf("%s %s cannot be negative", provider, maxTotalField)
	}
	if maxMachine != nil && *maxMachine <= 0 {
		return fmt.Errorf("%s %s must be positive", provider, maxMachineField)
	}
	if minMachine != nil && maxMachine != nil && *minMachine > *maxMachine {
		return fmt.Errorf("%s %s cannot exceed %s", provider, minMachineField, maxMachineField)
	}
	if minMachine != nil && poolDefault != nil && *poolDefault < *minMachine {
		return fmt.Errorf("%s %s cannot be lower than %s", provider, defaultField, minMachineField)
	}
	if poolDefault != nil && maxMachine != nil && *poolDefault > *maxMachine {
		return fmt.Errorf("%s %s cannot exceed %s", provider, defaultField, maxMachineField)
	}
	return nil
}

func validateResourceField(
	provider, field string,
	value *int,
	mode MachineResourceFieldMode,
) error {
	switch mode {
	case MachineResourceRequired:
		if value == nil {
			return fmt.Errorf("%s machine pools require %s", provider, field)
		}
	case MachineResourceOptional:
	case MachineResourceUnsupported:
		if value != nil {
			return fmt.Errorf("%s machine pools do not support %s", provider, field)
		}
	default:
		return fmt.Errorf("%s machine pool has invalid %s policy", provider, field)
	}
	return nil
}

func ValidateMachineProvisioningResourcePolicy(
	provider string,
	machine executionstore.MachineProvisioningConfig,
	resources MachineResourcePolicy,
) error {
	if err := validateMachineProvisioningResource(
		provider,
		"cpu",
		machine.CPU,
		resources.CPU.Provisioning,
	); err != nil {
		return err
	}
	return validateMachineProvisioningResource(
		provider,
		"memory_mb",
		machine.MemoryMB,
		resources.MemoryMB.Provisioning,
	)
}

func validateMachineProvisioningResource(
	provider, resource string,
	value *int,
	mode MachineResourceProvisioningMode,
) error {
	switch mode {
	case MachineResourceConfigured:
		if value == nil {
			return fmt.Errorf("%s machine config requires %s", provider, resource)
		}
	case MachineResourceProviderResolved:
	case MachineResourceUnmodeled:
		if value != nil {
			return fmt.Errorf("%s machine config does not support %s", provider, resource)
		}
	default:
		return fmt.Errorf("%s machine config has invalid %s policy", provider, resource)
	}
	if value != nil && *value <= 0 {
		return fmt.Errorf("%s %s must be positive", provider, resource)
	}
	return nil
}
