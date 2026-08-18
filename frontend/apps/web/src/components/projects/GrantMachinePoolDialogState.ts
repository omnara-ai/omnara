import type {
  CreateProjectMachinePoolGrantRequest,
  MachinePool,
  ProjectMachinePoolGrant,
  ProjectMachinePoolGrantListItem,
  UpdateProjectMachinePoolGrantRequest,
} from '@omnara/sdk'

import {
  emptyProviderOptions,
  envFromRows,
  envOverlayFromRows,
  type EnvOverlayRow,
  envOverlayRowsValid,
  newEnvOverlayRow,
  newSecretEnvOverlayRow,
  numberDraft,
  optionalInt,
  optionalIntOrNull,
  optionalNonNegativeInt32Valid,
  optionalPositiveInt32Valid,
  type ProviderOptionsDraft,
  providerOptionsOverlay,
  secretEnvFromRows,
  secretEnvOverlayFromRows,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
  stringOrUndefined,
} from '@/components/machines/machineOverrides'
import {
  isMachinePoolProvider,
  type MachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'
import {
  memoryGbDraft,
  memoryGbDraftValid,
  memoryGbToMb,
  memoryGbToMbPreservingOriginal,
} from '@/lib/machine-memory'

export interface PoolGrantOverrideDraft {
  description: string
  providerOptions: ProviderOptionsDraft
  cpu: string
  memoryGb: string
  cwd: string
  envRows: EnvOverlayRow[]
  secretEnvRows: SecretEnvOverlayRow[]
  maxTotalMachines: string
  maxTotalCpu: string
  maxTotalMemoryGb: string
  minMachineCpu: string
  minMachineMemoryGb: string
  maxMachineCpu: string
  maxMachineMemoryGb: string
  deleteAfterIdleMinutes: string
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
    memoryGb: '',
    cwd: '',
    envRows: [],
    secretEnvRows: [],
    maxTotalMachines: '',
    maxTotalCpu: '',
    maxTotalMemoryGb: '',
    minMachineCpu: '',
    minMachineMemoryGb: '',
    maxMachineCpu: '',
    maxMachineMemoryGb: '',
    deleteAfterIdleMinutes: '',
  }
}

export function poolGrantOverridesValid(draft: PoolGrantOverrideDraft) {
  return (
    envOverlayRowsValid(draft.envRows) &&
    secretEnvOverlayRowsValid(draft.secretEnvRows) &&
    [draft.cpu, draft.maxMachineCpu].every(optionalPositiveInt32Valid) &&
    optionalPositiveInt32Valid(draft.deleteAfterIdleMinutes) &&
    memoryGbDraftValid(draft.memoryGb, { optional: true }) &&
    memoryGbDraftValid(draft.maxMachineMemoryGb, { optional: true }) &&
    [draft.maxTotalMachines, draft.maxTotalCpu, draft.minMachineCpu].every(
      optionalNonNegativeInt32Valid,
    ) &&
    memoryGbDraftValid(draft.maxTotalMemoryGb, { optional: true, allowZero: true }) &&
    memoryGbDraftValid(draft.minMachineMemoryGb, { optional: true, allowZero: true })
  )
}

function envRowsFromOverlay(overlay: Record<string, string | null>): EnvOverlayRow[] {
  return Object.entries(overlay)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => ({ ...newEnvOverlayRow(), key, value }))
}

function secretEnvRowsFromOverlay(overlay: Record<string, string | null>): SecretEnvOverlayRow[] {
  return Object.entries(overlay)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, secretId]) => ({ ...newSecretEnvOverlayRow(), key, secretId }))
}

function editableProviderOptionKeys(provider: MachinePoolProvider, clusterManaged: boolean) {
  const definition = machinePoolProviderDefinitions[provider]
  const keys = new Set(['startup_script'])
  if (!clusterManaged) {
    keys.add(definition.resource.key)
    keys.add(definition.location.key)
  }
  return keys
}

function providerOptionsDraftFromOverlay(
  provider: MachinePoolProvider,
  overlay: Record<string, unknown>,
  clusterManaged: boolean,
): ProviderOptionsDraft {
  const definition = machinePoolProviderDefinitions[provider]
  const stringValue = (key: string) => {
    const value = overlay[key]
    return typeof value === 'string' ? value : ''
  }
  return {
    resource: clusterManaged ? '' : stringValue(definition.resource.key),
    location: clusterManaged ? '' : stringValue(definition.location.key),
    startupScript: stringValue('startup_script'),
  }
}

export function poolGrantDraftFromGrant(
  item: ProjectMachinePoolGrantListItem,
): PoolGrantOverrideDraft {
  const grant = item.grant
  const provider = isMachinePoolProvider(item.machine_pool.provider)
    ? item.machine_pool.provider
    : null
  return {
    description: grant.description,
    providerOptions: provider
      ? providerOptionsDraftFromOverlay(
          provider,
          grant.default_machine_provider_options_overlay,
          item.machine_pool.management_kind === 'cluster',
        )
      : emptyProviderOptions,
    cpu: numberDraft(grant.default_machine_cpu),
    memoryGb: memoryGbDraft(grant.default_machine_memory_mb),
    cwd: grant.default_cwd,
    envRows: envRowsFromOverlay(grant.default_machine_env_overlay),
    secretEnvRows: secretEnvRowsFromOverlay(grant.default_machine_secret_env_overlay),
    maxTotalMachines: numberDraft(grant.max_total_machines),
    maxTotalCpu: numberDraft(grant.max_total_cpu),
    maxTotalMemoryGb: memoryGbDraft(grant.max_total_memory_mb),
    minMachineCpu: numberDraft(grant.min_machine_cpu),
    minMachineMemoryGb: memoryGbDraft(grant.min_machine_memory_mb),
    maxMachineCpu: numberDraft(grant.max_machine_cpu),
    maxMachineMemoryGb: memoryGbDraft(grant.max_machine_memory_mb),
    deleteAfterIdleMinutes: numberDraft(grant.delete_after_idle_minutes),
  }
}

/**
 * Every field is prefilled from the stored grant and sent back explicitly:
 * emptied inputs and deleted rows clear the override so the grant follows the
 * pool again. Null overlay entries appear as unset rows; deleting one lets the
 * pool's value through again. Provider options outside the known fields are
 * carried over from the stored grant unchanged.
 */
export function poolGrantUpdateRequest(
  pool: MachinePool,
  grant: ProjectMachinePoolGrant,
  draft: PoolGrantOverrideDraft,
): UpdateProjectMachinePoolGrantRequest {
  const provider = isMachinePoolProvider(pool.provider) ? pool.provider : null
  const clusterManaged = pool.management_kind === 'cluster'
  let providerOptionsOverlayPatch: Record<string, unknown> | undefined
  if (provider) {
    const editableKeys = editableProviderOptionKeys(provider, clusterManaged)
    providerOptionsOverlayPatch = {
      ...Object.fromEntries(
        Object.entries(grant.default_machine_provider_options_overlay).filter(
          ([key]) => !editableKeys.has(key),
        ),
      ),
      ...(providerOptionsOverlay(provider, draft.providerOptions, clusterManaged) ?? {}),
    }
  }
  return {
    description: draft.description.trim(),
    default_machine_cpu: optionalIntOrNull(draft.cpu),
    default_machine_memory_mb: optionalMemoryMbOrNull(
      draft.memoryGb,
      grant.default_machine_memory_mb,
    ),
    default_machine_env_overlay: envOverlayFromRows(draft.envRows) ?? {},
    default_machine_secret_env_overlay: secretEnvOverlayFromRows(draft.secretEnvRows) ?? {},
    default_machine_provider_options_overlay: providerOptionsOverlayPatch,
    default_cwd: draft.cwd.trim(),
    max_total_machines: optionalIntOrNull(draft.maxTotalMachines),
    max_total_cpu: optionalIntOrNull(draft.maxTotalCpu),
    max_total_memory_mb: optionalMemoryMbOrNull(draft.maxTotalMemoryGb, grant.max_total_memory_mb),
    min_machine_cpu: optionalIntOrNull(draft.minMachineCpu),
    min_machine_memory_mb: optionalMemoryMbOrNull(
      draft.minMachineMemoryGb,
      grant.min_machine_memory_mb,
    ),
    max_machine_cpu: optionalIntOrNull(draft.maxMachineCpu),
    max_machine_memory_mb: optionalMemoryMbOrNull(
      draft.maxMachineMemoryGb,
      grant.max_machine_memory_mb,
    ),
    delete_after_idle_minutes: optionalIntOrNull(draft.deleteAfterIdleMinutes),
  }
}

export function poolGrantCreateRequest(
  pool: MachinePool,
  draft: PoolGrantOverrideDraft,
): CreateProjectMachinePoolGrantRequest {
  return {
    machine_pool_id: pool.id,
    description: stringOrUndefined(draft.description),
    default_machine_cpu: optionalInt(draft.cpu),
    default_machine_memory_mb: optionalMemoryMb(draft.memoryGb),
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
    max_total_memory_mb: optionalMemoryMb(draft.maxTotalMemoryGb),
    min_machine_cpu: optionalInt(draft.minMachineCpu),
    min_machine_memory_mb: optionalMemoryMb(draft.minMachineMemoryGb),
    max_machine_cpu: optionalInt(draft.maxMachineCpu),
    max_machine_memory_mb: optionalMemoryMb(draft.maxMachineMemoryGb),
    delete_after_idle_minutes: optionalInt(draft.deleteAfterIdleMinutes),
  }
}

function optionalMemoryMb(value: string) {
  return value.trim() === '' ? undefined : memoryGbToMb(value)
}

function optionalMemoryMbOrNull(value: string, originalMemoryMb: number | null) {
  if (value.trim() === '') return null
  return originalMemoryMb === null
    ? memoryGbToMb(value)
    : memoryGbToMbPreservingOriginal(value, originalMemoryMb)
}
