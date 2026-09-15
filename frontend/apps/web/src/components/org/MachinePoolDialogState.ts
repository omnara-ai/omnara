import type { CreateMachinePoolRequest, MachinePool, UpdateMachinePoolRequest } from '@omnara/sdk'

import {
  envFromRows,
  type EnvOverlayRow,
  envOverlayRowsValid,
  numberDraft,
  optionalInt,
  optionalIntOrNull,
  optionalNonNegativeInt32Valid,
  optionalPoolIdleDeletionMinutesValid,
  optionalPositiveInt32Valid,
  secretEnvFromRows,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
  stringOrUndefined,
} from '@/components/machines/machineOverrides'
import { memoryGbDraft, memoryGbDraftValid, memoryGbToMb } from '@/lib/machine-memory'
import { providerOptionStrings } from '@/lib/provider-options'
import { resourceNameValid } from '@/lib/resource-name'

import {
  envRowsFromRecord,
  memoryMbFromDraft,
  optionalMemoryMb,
  optionalMemoryMbOrNull,
  optionalMemoryMbPreservingOriginal,
  secretEnvRowsFromRecord,
} from './machinePoolFormHelpers'
import {
  isMachinePoolProvider,
  type MachinePoolProvider,
  machinePoolProviderDefinitions,
} from './machinePoolProviders'
import {
  effectiveMachineSizeDrafts,
  machineSizeOmitted,
  machineSizeValid,
  smallestMachineSizeDrafts,
} from './machinePoolSizes'

export const machinePoolProviders = Object.entries(machinePoolProviderDefinitions).map(
  ([value, definition]) => ({ value, label: definition.label }),
)

export function machinePoolProviderLabel(provider: string) {
  return machinePoolProviders.find((option) => option.value === provider)?.label ?? provider
}

export interface MachinePoolFormValues {
  name: string
  description: string
  provider: MachinePoolProvider
  providerScope: string
  image: string
  location: string
  startupScript: string
  cwd: string
  envRows: EnvOverlayRow[]
  secretEnvRows: SecretEnvOverlayRow[]
  cpu: string
  memoryGb: string
  maxMachines: string
  /** Optional caps; empty derives from machine size × max machines. */
  maxTotalCpu: string
  maxTotalMemoryGb: string
  minMachineCpu: string
  minMachineMemoryGb: string
  maxMachineCpu: string
  maxMachineMemoryGb: string
  deleteAfterIdleMinutes: string
  /** Secret id; '' until one is selected. */
  secretId: string
  projectGrantIds: string[]
  runtimeProtectionEnabled: boolean
}

export type MachinePoolFormMode = 'create' | 'tenant-edit' | 'cluster-edit'

export const machinePoolFormDefaults: MachinePoolFormValues = {
  name: '',
  description: '',
  provider: 'blaxel',
  providerScope: '',
  image: '',
  location: machinePoolProviderDefinitions.blaxel.location?.defaultValue ?? '',
  startupScript: '',
  cwd: '',
  envRows: [],
  secretEnvRows: [],
  cpu: '1',
  memoryGb: '1',
  maxMachines: '3',
  maxTotalCpu: '',
  maxTotalMemoryGb: '',
  minMachineCpu: '',
  minMachineMemoryGb: '',
  maxMachineCpu: '',
  maxMachineMemoryGb: '',
  deleteAfterIdleMinutes: '',
  secretId: '',
  projectGrantIds: [],
  runtimeProtectionEnabled: false,
}

const maxInt32 = 2_147_483_647

function positiveInt32(value: string) {
  const parsed = Number(value)
  return value.trim() !== '' && Number.isInteger(parsed) && parsed > 0 && parsed <= maxInt32
}

function nonNegativeInt32(value: string) {
  const parsed = Number(value)
  return value.trim() !== '' && Number.isInteger(parsed) && parsed >= 0 && parsed <= maxInt32
}

function aggregateFitsInt32(perMachine: string, maxMachines: string) {
  const perMachineValue = Number(perMachine)
  const maxMachinesValue = Number(maxMachines)
  return perMachineValue <= Math.floor(maxInt32 / maxMachinesValue)
}

function memoryAggregateFitsInt32(perMachineGb: string, maxMachines: string) {
  return memoryGbToMb(perMachineGb) <= Math.floor(maxInt32 / Number(maxMachines))
}

/** Placeholder for an aggregate cap input: machine size × max machines. */
export function derivedTotalCapPlaceholder(perMachine: string, maxMachines: string) {
  if (!positiveInt32(perMachine) || !nonNegativeInt32(maxMachines)) return undefined
  if (!aggregateFitsInt32(perMachine, maxMachines)) return undefined
  return String(Number(perMachine) * Number(maxMachines))
}

export function derivedMemoryTotalCapPlaceholder(perMachineGb: string, maxMachines: string) {
  if (!memoryGbDraftValid(perMachineGb) || !nonNegativeInt32(maxMachines)) return undefined
  const perMachineMb = memoryGbToMb(perMachineGb)
  if (!memoryAggregateFitsInt32(perMachineGb, maxMachines)) return undefined
  return memoryGbDraft(perMachineMb * Number(maxMachines))
}

export function machinePoolFormAfterProviderChange(
  values: MachinePoolFormValues,
  provider: MachinePoolProvider,
): MachinePoolFormValues {
  if (provider === values.provider) return values
  const currentDefinition = machinePoolProviderDefinitions[values.provider]
  const nextDefinition = machinePoolProviderDefinitions[provider]
  const sized = smallestMachineSizeDrafts(provider)
  return {
    ...values,
    provider,
    providerScope: '',
    image: '',
    location: nextDefinition.location?.defaultValue ?? '',
    cpu:
      sized?.cpu ??
      (currentDefinition.resources.cpu === nextDefinition.resources.cpu
        ? values.cpu
        : machinePoolFormDefaults.cpu),
    memoryGb:
      sized?.memoryGb ??
      (currentDefinition.resources.memoryMb === nextDefinition.resources.memoryMb
        ? values.memoryGb
        : machinePoolFormDefaults.memoryGb),
    maxTotalCpu: '',
    maxTotalMemoryGb: '',
    minMachineCpu: '',
    minMachineMemoryGb: '',
    maxMachineCpu: '',
    maxMachineMemoryGb: '',
    secretId: '',
  }
}

export function machinePoolFormValid(
  values: MachinePoolFormValues,
  mode: MachinePoolFormMode = 'create',
) {
  const provider = machinePoolProviderDefinitions[values.provider]
  const clusterEdit = mode === 'cluster-edit'
  const maxMachinesValid = clusterEdit || nonNegativeInt32(values.maxMachines)
  // With the size left to the provider, the per-machine caps stand in for it.
  const { cpu, memoryGb } = effectiveMachineSizeDrafts(values.provider, values)
  const cpuValid =
    provider.resources.cpu === 'unsupported' ||
    (positiveInt32(cpu) &&
      (clusterEdit ||
        (maxMachinesValid &&
          (values.maxTotalCpu.trim() !== '' || aggregateFitsInt32(cpu, values.maxMachines)))))
  const memoryValid =
    provider.resources.memoryMb === 'unsupported' ||
    (memoryGbDraftValid(memoryGb) &&
      (clusterEdit ||
        (maxMachinesValid &&
          (values.maxTotalMemoryGb.trim() !== '' ||
            memoryAggregateFitsInt32(memoryGb, values.maxMachines)))))
  return (
    machineSizeValid(values.provider, values) &&
    (clusterEdit ||
      (resourceNameValid(values.name) &&
        (provider.resource.optional === true || values.image.trim() !== '') &&
        (!provider.location?.required || values.location.trim() !== '') &&
        (!provider.scope?.required || values.providerScope.trim() !== '') &&
        values.secretId !== '')) &&
    maxMachinesValid &&
    cpuValid &&
    memoryValid &&
    envOverlayRowsValid(values.envRows) &&
    secretEnvOverlayRowsValid(values.secretEnvRows) &&
    optionalPositiveInt32Valid(values.maxMachineCpu) &&
    optionalPoolIdleDeletionMinutesValid(values.deleteAfterIdleMinutes) &&
    memoryGbDraftValid(values.maxMachineMemoryGb, { optional: true }) &&
    (clusterEdit || optionalNonNegativeInt32Valid(values.maxTotalCpu)) &&
    optionalNonNegativeInt32Valid(values.minMachineCpu) &&
    (clusterEdit ||
      memoryGbDraftValid(values.maxTotalMemoryGb, { optional: true, allowZero: true })) &&
    memoryGbDraftValid(values.minMachineMemoryGb, { optional: true, allowZero: true })
  )
}

export function machinePoolCreateRequest(values: MachinePoolFormValues): CreateMachinePoolRequest {
  const sizeOmitted = machineSizeOmitted(values.provider, values)
  const size = effectiveMachineSizeDrafts(values.provider, values)
  const cpu = Number(size.cpu)
  const memoryMb = memoryGbToMb(size.memoryGb)
  const maxMachines = Number(values.maxMachines)
  const common = {
    name: values.name,
    description: stringOrUndefined(values.description),
    provider_auth_secret_id: values.secretId,
    max_total_machines: maxMachines,
    default_machine_env: envFromRows(values.envRows),
    default_machine_secret_env: secretEnvFromRows(values.secretEnvRows),
    default_machine_provider_options: providerOptionsFromForm(values),
    default_cwd: stringOrUndefined(values.cwd),
    runtime_protection_enabled: values.runtimeProtectionEnabled,
    delete_after_idle_minutes: optionalInt(values.deleteAfterIdleMinutes),
  }
  const cpuCaps = {
    max_total_cpu: optionalInt(values.maxTotalCpu) ?? cpu * maxMachines,
    min_machine_cpu: optionalInt(values.minMachineCpu),
    max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
  }
  const memoryCaps = {
    max_total_memory_mb: optionalMemoryMb(values.maxTotalMemoryGb) ?? memoryMb * maxMachines,
    min_machine_memory_mb: optionalMemoryMb(values.minMachineMemoryGb),
    max_machine_memory_mb: optionalMemoryMb(values.maxMachineMemoryGb) ?? memoryMb,
  }
  switch (values.provider) {
    case 'unikraft':
    case 'modal':
      return {
        ...common,
        provider: values.provider,
        provider_config:
          values.provider === 'modal' ? { app: values.providerScope.trim() } : undefined,
        default_machine_cpu: cpu,
        default_machine_memory_mb: memoryMb,
        ...cpuCaps,
        ...memoryCaps,
      }
    case 'boxd':
      // An omitted size is left to the snapshot or the boxd org default.
      return {
        ...common,
        provider: 'boxd',
        default_machine_cpu: sizeOmitted ? undefined : cpu,
        default_machine_memory_mb: sizeOmitted ? undefined : memoryMb,
        ...cpuCaps,
        ...memoryCaps,
      }
    case 'blaxel':
      return {
        ...common,
        provider: 'blaxel',
        default_machine_memory_mb: memoryMb,
        provider_config: { workspace: values.providerScope.trim() },
        ...memoryCaps,
      }
    case 'daytona':
      return { ...common, provider: 'daytona', ...cpuCaps, ...memoryCaps }
  }
}

/**
 * The provider option keys the form edits. An optional resource or location left empty is
 * omitted so the provider applies its own default.
 */
function providerOptionsFromForm(values: MachinePoolFormValues) {
  const definition = machinePoolProviderDefinitions[values.provider]
  const options: Record<string, string> = {}
  const resource = values.image.trim()
  if (resource !== '' || definition.resource.optional !== true) {
    options[definition.resource.key] = resource
  }
  const location = values.location.trim()
  if (definition.location && (location !== '' || definition.location.required)) {
    options[definition.location.key] = location
  }
  if (values.startupScript.trim() !== '') options.startup_script = values.startupScript
  return options
}

export function machinePoolFormFromPool(pool: MachinePool): MachinePoolFormValues | null {
  if (!isMachinePoolProvider(pool.provider)) return null
  const provider = pool.provider
  const definition = machinePoolProviderDefinitions[provider]
  const options = providerOptionStrings(pool.default_machine_provider_options)
  const cpuValue =
    definition.resources.cpu === 'provider-resolved'
      ? pool.max_machine_cpu
      : pool.default_machine_cpu
  const memoryValue =
    definition.resources.memoryMb === 'provider-resolved'
      ? pool.max_machine_memory_mb
      : pool.default_machine_memory_mb
  const sizeLeftToProvider =
    definition.sizes?.providerDefault !== undefined && cpuValue == null && memoryValue == null
  return {
    name: pool.name,
    description: pool.description,
    provider,
    providerScope:
      (definition.scope
        ? providerOptionStrings(pool.provider_config)[definition.scope.key]
        : undefined) ?? '',
    image: options[definition.resource.key] ?? '',
    location: definition.location ? (options[definition.location.key] ?? '') : '',
    startupScript: options.startup_script ?? '',
    cwd: pool.default_cwd,
    envRows: envRowsFromRecord(pool.default_machine_env),
    secretEnvRows: secretEnvRowsFromRecord(pool.default_machine_secret_env),
    cpu: sizeLeftToProvider ? '' : numberDraft(cpuValue) || machinePoolFormDefaults.cpu,
    memoryGb: sizeLeftToProvider
      ? ''
      : memoryGbDraft(memoryValue) || machinePoolFormDefaults.memoryGb,
    maxMachines: numberDraft(pool.max_total_machines),
    maxTotalCpu: numberDraft(pool.max_total_cpu),
    maxTotalMemoryGb: memoryGbDraft(pool.max_total_memory_mb),
    minMachineCpu: numberDraft(pool.min_machine_cpu),
    minMachineMemoryGb: memoryGbDraft(pool.min_machine_memory_mb),
    maxMachineCpu:
      definition.resources.cpu === 'provider-resolved' ? '' : numberDraft(pool.max_machine_cpu),
    maxMachineMemoryGb:
      definition.resources.memoryMb === 'provider-resolved'
        ? ''
        : memoryGbDraft(pool.max_machine_memory_mb),
    deleteAfterIdleMinutes: numberDraft(pool.delete_after_idle_minutes),
    secretId: pool.provider_auth_secret_id ?? '',
    projectGrantIds: [],
    runtimeProtectionEnabled: pool.runtime_protection_enabled,
  }
}

export function machinePoolUpdateRequest(
  pool: MachinePool,
  values: MachinePoolFormValues,
): UpdateMachinePoolRequest {
  if (pool.provider !== values.provider) throw new Error('machine pool provider cannot be changed')
  if (pool.management_kind === 'cluster') return clusterMachinePoolUpdateRequest(pool, values)
  const definition = machinePoolProviderDefinitions[values.provider]
  const editableOptionKeys = new Set([definition.resource.key, 'startup_script'])
  if (definition.location) editableOptionKeys.add(definition.location.key)
  const defaultMachineProviderOptions = {
    ...Object.fromEntries(
      Object.entries(pool.default_machine_provider_options).filter(
        ([key]) => !editableOptionKeys.has(key),
      ),
    ),
    ...providerOptionsFromForm(values),
  }
  const { sizeOmitted, cpu, memoryMb } = machineSizeForUpdate(pool, values)
  const maxMachines = Number(values.maxMachines)
  const common = {
    name: values.name,
    description: values.description.trim(),
    default_machine_env: envFromRows(values.envRows) ?? {},
    default_machine_secret_env: secretEnvFromRows(values.secretEnvRows) ?? {},
    default_machine_provider_options: defaultMachineProviderOptions,
    default_cwd: values.cwd.trim(),
    provider_auth_secret_id: values.secretId,
    runtime_protection_enabled: values.runtimeProtectionEnabled,
    max_total_machines: maxMachines,
    delete_after_idle_minutes: optionalIntOrNull(values.deleteAfterIdleMinutes),
  }
  switch (values.provider) {
    case 'unikraft':
    case 'modal':
    case 'boxd':
      return {
        ...common,
        provider_config:
          values.provider === 'modal'
            ? { ...pool.provider_config, app: values.providerScope.trim() }
            : undefined,
        default_machine_cpu: sizeOmitted ? null : cpu,
        default_machine_memory_mb: sizeOmitted ? null : memoryMb,
        max_total_cpu: optionalInt(values.maxTotalCpu) ?? cpu * maxMachines,
        max_total_memory_mb:
          optionalMemoryMbPreservingOriginal(values.maxTotalMemoryGb, pool.max_total_memory_mb) ??
          memoryMb * maxMachines,
        min_machine_cpu: optionalIntOrNull(values.minMachineCpu),
        min_machine_memory_mb: optionalMemoryMbOrNull(
          values.minMachineMemoryGb,
          pool.min_machine_memory_mb,
        ),
        max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
        max_machine_memory_mb:
          optionalMemoryMbPreservingOriginal(
            values.maxMachineMemoryGb,
            pool.max_machine_memory_mb,
          ) ?? memoryMb,
      }
    case 'blaxel':
      return {
        ...common,
        default_machine_memory_mb: memoryMb,
        provider_config: { ...pool.provider_config, workspace: values.providerScope.trim() },
        max_total_memory_mb:
          optionalMemoryMbPreservingOriginal(values.maxTotalMemoryGb, pool.max_total_memory_mb) ??
          memoryMb * maxMachines,
        min_machine_memory_mb: optionalMemoryMbOrNull(
          values.minMachineMemoryGb,
          pool.min_machine_memory_mb,
        ),
        max_machine_memory_mb:
          optionalMemoryMbPreservingOriginal(
            values.maxMachineMemoryGb,
            pool.max_machine_memory_mb,
          ) ?? memoryMb,
      }
    case 'daytona':
      return {
        ...common,
        max_total_cpu: optionalInt(values.maxTotalCpu) ?? cpu * maxMachines,
        max_total_memory_mb:
          optionalMemoryMbPreservingOriginal(values.maxTotalMemoryGb, pool.max_total_memory_mb) ??
          memoryMb * maxMachines,
        min_machine_cpu: optionalIntOrNull(values.minMachineCpu),
        min_machine_memory_mb: optionalMemoryMbOrNull(
          values.minMachineMemoryGb,
          pool.min_machine_memory_mb,
        ),
        max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
        max_machine_memory_mb:
          optionalMemoryMbPreservingOriginal(
            values.maxMachineMemoryGb,
            pool.max_machine_memory_mb,
          ) ?? memoryMb,
      }
  }
}

function clusterMachinePoolUpdateRequest(
  pool: MachinePool,
  values: MachinePoolFormValues,
): UpdateMachinePoolRequest {
  const { sizeOmitted, cpu, memoryMb } = machineSizeForUpdate(pool, values)
  const common = {
    default_machine_env: envFromRows(values.envRows) ?? {},
    default_machine_secret_env: secretEnvFromRows(values.secretEnvRows) ?? {},
    delete_after_idle_minutes: optionalIntOrNull(values.deleteAfterIdleMinutes),
    min_machine_memory_mb: optionalMemoryMbOrNull(
      values.minMachineMemoryGb,
      pool.min_machine_memory_mb,
    ),
  }
  switch (values.provider) {
    case 'unikraft':
    case 'modal':
    case 'boxd':
      return {
        ...common,
        default_machine_cpu: sizeOmitted ? null : cpu,
        default_machine_memory_mb: sizeOmitted ? null : memoryMb,
        min_machine_cpu: optionalIntOrNull(values.minMachineCpu),
        max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
        max_machine_memory_mb:
          optionalMemoryMbPreservingOriginal(
            values.maxMachineMemoryGb,
            pool.max_machine_memory_mb,
          ) ?? memoryMb,
      }
    case 'blaxel':
      return {
        ...common,
        default_machine_memory_mb: memoryMb,
        max_machine_memory_mb:
          optionalMemoryMbPreservingOriginal(
            values.maxMachineMemoryGb,
            pool.max_machine_memory_mb,
          ) ?? memoryMb,
      }
    case 'daytona':
      return {
        ...common,
        min_machine_cpu: optionalIntOrNull(values.minMachineCpu),
        max_machine_cpu: cpu,
        max_machine_memory_mb: memoryMb,
      }
  }
}

/** The machine size an update carries, converted against the pool's stored value. */
function machineSizeForUpdate(pool: MachinePool, values: MachinePoolFormValues) {
  const definition = machinePoolProviderDefinitions[values.provider]
  const sizeOmitted = machineSizeOmitted(values.provider, values)
  const size = effectiveMachineSizeDrafts(values.provider, values)
  const originalMemoryMb =
    sizeOmitted || definition.resources.memoryMb === 'provider-resolved'
      ? pool.max_machine_memory_mb
      : pool.default_machine_memory_mb
  return {
    sizeOmitted,
    cpu: Number(size.cpu),
    memoryMb: memoryMbFromDraft(size.memoryGb, originalMemoryMb),
  }
}
