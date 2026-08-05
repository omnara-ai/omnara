package executionstore

import (
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type MachineResourceLimits struct {
	MaxTotalCPU        *int
	MaxTotalMemoryMB   *int
	MaxMachineCPU      *int
	MaxMachineMemoryMB *int
}

func knownResourceCaps(resources MachinePoolResources, caps MachineResourceLimits) MachineResourceLimits {
	if resources.CPU == 0 {
		caps.MaxTotalCPU = nil
		caps.MaxMachineCPU = nil
	}
	if resources.MemoryMB == 0 {
		caps.MaxTotalMemoryMB = nil
		caps.MaxMachineMemoryMB = nil
	}
	return caps
}

func effectivePoolGrantCap(poolCap int32, grantCap *int32) int {
	if grantCap == nil {
		return int(poolCap)
	}
	return min(int(poolCap), int(*grantCap))
}

func effectiveOptionalPoolGrantCap(poolCap, grantCap *int32) *int {
	if poolCap == nil {
		return nil
	}
	value := effectivePoolGrantCap(*poolCap, grantCap)
	return &value
}

func validateMachineResourcesWithinPerMachineLimits(
	machineResources MachinePoolResources,
	perMachineLimits MachineResourceLimits,
) error {
	if perMachineLimits.MaxMachineCPU != nil &&
		machineResources.CPU > int64(*perMachineLimits.MaxMachineCPU) {
		return errors.New("cpu exceeds max_machine_cpu")
	}
	if perMachineLimits.MaxMachineMemoryMB != nil &&
		machineResources.MemoryMB > int64(*perMachineLimits.MaxMachineMemoryMB) {
		return errors.New("memory_mb exceeds max_machine_memory_mb")
	}
	return nil
}

func validateProjectMachinePoolGrantStaticPolicy(
	config projectMachinePoolGrantConfig,
	pool MachinePoolRecord,
) error {
	if config.MaxTotalCPU != nil && pool.MaxTotalCPU == nil {
		return errors.New("pool grant max_total_cpu is not supported by the machine pool")
	}
	if config.MaxTotalMemoryMB != nil && pool.MaxTotalMemoryMB == nil {
		return errors.New("pool grant max_total_memory_mb is not supported by the machine pool")
	}
	if config.MaxMachineCPU != nil {
		if pool.MaxMachineCPU == nil {
			return errors.New("pool grant max_machine_cpu is not supported by the machine pool")
		}
		if *config.MaxMachineCPU > *pool.MaxMachineCPU {
			return errors.New("pool grant max_machine_cpu cannot exceed machine pool max_machine_cpu")
		}
	}
	if config.MaxMachineMemoryMB != nil {
		if pool.MaxMachineMemoryMB == nil {
			return errors.New("pool grant max_machine_memory_mb is not supported by the machine pool")
		}
		if *config.MaxMachineMemoryMB > *pool.MaxMachineMemoryMB {
			return errors.New("pool grant max_machine_memory_mb cannot exceed machine pool max_machine_memory_mb")
		}
	}
	return nil
}

func resolveProjectMachinePoolGrantPerMachineLimits(
	pool MachinePoolRecord,
	config projectMachinePoolGrantConfig,
) MachineResourceLimits {
	perMachineLimits := MachineResourceLimits{
		MaxMachineCPU:      pool.MaxMachineCPU,
		MaxMachineMemoryMB: pool.MaxMachineMemoryMB,
	}
	if config.MaxMachineCPU != nil {
		perMachineLimits.MaxMachineCPU = config.MaxMachineCPU
	}
	if config.MaxMachineMemoryMB != nil {
		perMachineLimits.MaxMachineMemoryMB = config.MaxMachineMemoryMB
	}
	return perMachineLimits
}

func checkLaunchResourceCaps(
	currentCPU int64,
	currentMemoryMB int64,
	requested MachinePoolResources,
	requestedMachines int,
	caps MachineResourceLimits,
) error {
	if err := checkLaunchPerMachineCap(requested.CPU, caps.MaxMachineCPU); err != nil {
		return fmt.Errorf("per-machine cpu capacity exceeded: %w", err)
	}
	if err := checkLaunchPerMachineCap(requested.MemoryMB, caps.MaxMachineMemoryMB); err != nil {
		return fmt.Errorf("per-machine memory capacity exceeded: %w", err)
	}
	if err := checkLaunchAggregateCap(currentCPU, requested.CPU, requestedMachines, caps.MaxTotalCPU); err != nil {
		return fmt.Errorf("cpu capacity exceeded: %w", err)
	}
	if err := checkLaunchAggregateCap(
		currentMemoryMB,
		requested.MemoryMB,
		requestedMachines,
		caps.MaxTotalMemoryMB,
	); err != nil {
		return fmt.Errorf("memory capacity exceeded: %w", err)
	}
	return nil
}

func checkLaunchAggregateCap(current, perMachine int64, requestedMachines int, limit *int) error {
	if limit == nil {
		return nil
	}
	if perMachine <= 0 {
		return fmt.Errorf("machine config does not define the capped resource: %w", storeerr.ErrStateTransitionConflict)
	}
	if current+perMachine*int64(requestedMachines) > int64(*limit) {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func checkLaunchPerMachineCap(perMachine int64, limit *int) error {
	if limit == nil {
		return nil
	}
	if perMachine <= 0 {
		return fmt.Errorf("machine config does not define the capped resource: %w", storeerr.ErrStateTransitionConflict)
	}
	if perMachine > int64(*limit) {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func checkProvisioningResourceAdmission(
	currentCPU, currentMemoryMB int64,
	currentMachine, resolved MachinePoolResources,
	caps MachineResourceLimits,
) error {
	if err := checkLaunchPerMachineCap(resolved.CPU, caps.MaxMachineCPU); err != nil {
		return fmt.Errorf("per-machine cpu capacity exceeded: %w", err)
	}
	if err := checkLaunchPerMachineCap(resolved.MemoryMB, caps.MaxMachineMemoryMB); err != nil {
		return fmt.Errorf("per-machine memory capacity exceeded: %w", err)
	}
	if currentMachine.CPU > resolved.CPU || currentMachine.MemoryMB > resolved.MemoryMB {
		return fmt.Errorf("resolved machine resources changed: %w", storeerr.ErrStateTransitionConflict)
	}
	if caps.MaxTotalCPU != nil &&
		currentCPU+resolved.CPU-currentMachine.CPU > int64(*caps.MaxTotalCPU) {
		return fmt.Errorf("cpu capacity exceeded: %w", storeerr.ErrStateTransitionConflict)
	}
	if caps.MaxTotalMemoryMB != nil &&
		currentMemoryMB+resolved.MemoryMB-currentMachine.MemoryMB > int64(*caps.MaxTotalMemoryMB) {
		return fmt.Errorf("memory capacity exceeded: %w", storeerr.ErrStateTransitionConflict)
	}
	return nil
}
