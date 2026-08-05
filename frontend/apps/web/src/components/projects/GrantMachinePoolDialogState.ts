import type { CreateProjectMachinePoolGrantRequest, MachinePool } from '@omnara/sdk'

import {
  emptyProviderOptions,
  envFromRows,
  type EnvOverlayRow,
  envOverlayRowsValid,
  optionalInt,
  optionalNonNegativeInt32Valid,
  optionalPositiveInt32Valid,
  type ProviderOptionsDraft,
  providerOptionsOverlay,
  secretEnvFromRows,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
  stringOrUndefined,
} from '@/components/machines/machineOverrides'
import { isMachinePoolProvider } from '@/components/org/machinePoolProviders'

export interface PoolGrantOverrideDraft {
  description: string
  providerOptions: ProviderOptionsDraft
  cpu: string
  memoryMb: string
  cwd: string
  envRows: EnvOverlayRow[]
  secretEnvRows: SecretEnvOverlayRow[]
  maxTotalMachines: string
  maxTotalCpu: string
  maxTotalMemoryMb: string
  maxMachineCpu: string
  maxMachineMemoryMb: string
}

/**
 * Every override input starts empty and inherits from the pool; entries you
 * add override or extend the pool's values for this grant.
 */
export function emptyPoolGrantDraft(): PoolGrantOverrideDraft {
  return {
    description: '',
    providerOptions: emptyProviderOptions,
    cpu: '',
    memoryMb: '',
    cwd: '',
    envRows: [],
    secretEnvRows: [],
    maxTotalMachines: '',
    maxTotalCpu: '',
    maxTotalMemoryMb: '',
    maxMachineCpu: '',
    maxMachineMemoryMb: '',
  }
}

export function poolGrantOverridesValid(draft: PoolGrantOverrideDraft) {
  return (
    envOverlayRowsValid(draft.envRows) &&
    secretEnvOverlayRowsValid(draft.secretEnvRows) &&
    [draft.cpu, draft.memoryMb, draft.maxMachineCpu, draft.maxMachineMemoryMb].every(
      optionalPositiveInt32Valid,
    ) &&
    [draft.maxTotalMachines, draft.maxTotalCpu, draft.maxTotalMemoryMb].every(
      optionalNonNegativeInt32Valid,
    )
  )
}

export function poolGrantCreateRequest(
  pool: MachinePool,
  draft: PoolGrantOverrideDraft,
): CreateProjectMachinePoolGrantRequest {
  return {
    machine_pool_id: pool.id,
    description: stringOrUndefined(draft.description),
    default_machine_cpu: optionalInt(draft.cpu),
    default_machine_memory_mb: optionalInt(draft.memoryMb),
    default_machine_env_overlay: envFromRows(draft.envRows),
    default_machine_secret_env_overlay: secretEnvFromRows(draft.secretEnvRows),
    default_machine_provider_options_overlay: isMachinePoolProvider(pool.provider)
      ? providerOptionsOverlay(
          pool.provider,
          draft.providerOptions,
          pool.management_kind === 'cluster',
        )
      : undefined,
    default_cwd: stringOrUndefined(draft.cwd),
    max_total_machines: optionalInt(draft.maxTotalMachines),
    max_total_cpu: optionalInt(draft.maxTotalCpu),
    max_total_memory_mb: optionalInt(draft.maxTotalMemoryMb),
    max_machine_cpu: optionalInt(draft.maxMachineCpu),
    max_machine_memory_mb: optionalInt(draft.maxMachineMemoryMb),
  }
}
