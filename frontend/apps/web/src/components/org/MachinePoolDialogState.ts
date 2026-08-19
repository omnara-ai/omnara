import type { CreateMachinePoolRequest, MachinePool, UpdateMachinePoolRequest } from '@omnara/sdk'

import {
  envFromRows,
  type EnvOverlayRow,
  envOverlayRowsValid,
  newEnvOverlayRow,
  newSecretEnvOverlayRow,
  numberDraft,
  optionalInt,
  optionalIntOrNull,
  optionalNonNegativeInt32Valid,
  optionalPositiveInt32Valid,
  secretEnvFromRows,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
  stringOrUndefined,
} from '@/components/machines/machineOverrides'
import {
  memoryGbDraft,
  memoryGbDraftValid,
  memoryGbToMb,
  memoryGbToMbPreservingOriginal,
} from '@/lib/machine-memory'

import {
  isMachinePoolProvider,
  type MachinePoolProvider,
  machinePoolProviderDefinitions,
} from './machinePoolProviders'

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
  workspace: string
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
  workspace: '',
  image: '',
  location: machinePoolProviderDefinitions.blaxel.location.defaultValue,
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
  return {
    ...values,
    provider,
    workspace: '',
    image: '',
    location: nextDefinition.location.defaultValue,
    cpu:
      currentDefinition.resources.cpu === nextDefinition.resources.cpu
        ? values.cpu
        : machinePoolFormDefaults.cpu,
    memoryGb:
      currentDefinition.resources.memoryMb === nextDefinition.resources.memoryMb
        ? values.memoryGb
        : machinePoolFormDefaults.memoryGb,
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
  const cpuValid =
    provider.resources.cpu === 'unsupported' ||
    (positiveInt32(values.cpu) &&
      (clusterEdit ||
        (maxMachinesValid &&
          (values.maxTotalCpu.trim() !== '' ||
            aggregateFitsInt32(values.cpu, values.maxMachines)))))
  const memoryValid =
    provider.resources.memoryMb === 'unsupported' ||
    (memoryGbDraftValid(values.memoryGb) &&
      (clusterEdit ||
        (maxMachinesValid &&
          (values.maxTotalMemoryGb.trim() !== '' ||
            memoryAggregateFitsInt32(values.memoryGb, values.maxMachines)))))
  return (
    (clusterEdit ||
      (values.name.trim() !== '' &&
        values.image.trim() !== '' &&
        values.location.trim() !== '' &&
        (!provider.requiresWorkspace || values.workspace.trim() !== '') &&
        values.secretId !== '')) &&
    maxMachinesValid &&
    cpuValid &&
    memoryValid &&
    envOverlayRowsValid(values.envRows) &&
    secretEnvOverlayRowsValid(values.secretEnvRows) &&
    optionalPositiveInt32Valid(values.maxMachineCpu) &&
    memoryGbDraftValid(values.maxMachineMemoryGb, { optional: true }) &&
    (clusterEdit || optionalNonNegativeInt32Valid(values.maxTotalCpu)) &&
    optionalNonNegativeInt32Valid(values.minMachineCpu) &&
    (clusterEdit ||
      memoryGbDraftValid(values.maxTotalMemoryGb, { optional: true, allowZero: true })) &&
    memoryGbDraftValid(values.minMachineMemoryGb, { optional: true, allowZero: true })
  )
}

export function machinePoolCreateRequest(values: MachinePoolFormValues): CreateMachinePoolRequest {
  const cpu = Number(values.cpu)
  const memoryMb = memoryGbToMb(values.memoryGb)
  const maxMachines = Number(values.maxMachines)
  const common = {
    name: values.name.trim(),
    description: stringOrUndefined(values.description),
    provider_auth_secret_id: values.secretId,
    max_total_machines: maxMachines,
    default_machine_env: envFromRows(values.envRows),
    default_machine_secret_env: secretEnvFromRows(values.secretEnvRows),
    default_cwd: stringOrUndefined(values.cwd),
    runtime_protection_enabled: values.runtimeProtectionEnabled,
  }
  const startupScript =
    values.startupScript.trim() === '' ? {} : { startup_script: values.startupScript }
  switch (values.provider) {
    case 'unikraft':
      return {
        ...common,
        provider: 'unikraft',
        default_machine_cpu: cpu,
        default_machine_memory_mb: memoryMb,
        default_machine_provider_options: {
          image: values.image.trim(),
          metro: values.location.trim(),
          ...startupScript,
        },
        max_total_cpu: optionalInt(values.maxTotalCpu) ?? cpu * maxMachines,
        max_total_memory_mb: optionalMemoryMb(values.maxTotalMemoryGb) ?? memoryMb * maxMachines,
        min_machine_cpu: optionalInt(values.minMachineCpu),
        min_machine_memory_mb: optionalMemoryMb(values.minMachineMemoryGb),
        max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
        max_machine_memory_mb: optionalMemoryMb(values.maxMachineMemoryGb) ?? memoryMb,
      }
    case 'blaxel':
      return {
        ...common,
        provider: 'blaxel',
        default_machine_memory_mb: memoryMb,
        default_machine_provider_options: {
          image: values.image.trim(),
          region: values.location.trim(),
          ...startupScript,
        },
        provider_config: { workspace: values.workspace.trim() },
        max_total_memory_mb: optionalMemoryMb(values.maxTotalMemoryGb) ?? memoryMb * maxMachines,
        min_machine_memory_mb: optionalMemoryMb(values.minMachineMemoryGb),
        max_machine_memory_mb: optionalMemoryMb(values.maxMachineMemoryGb) ?? memoryMb,
      }
    case 'daytona':
      return {
        ...common,
        provider: 'daytona',
        default_machine_provider_options: {
          snapshot: values.image.trim(),
          target: values.location.trim(),
          ...startupScript,
        },
        max_total_cpu: optionalInt(values.maxTotalCpu) ?? cpu * maxMachines,
        max_total_memory_mb: optionalMemoryMb(values.maxTotalMemoryGb) ?? memoryMb * maxMachines,
        min_machine_cpu: optionalInt(values.minMachineCpu),
        min_machine_memory_mb: optionalMemoryMb(values.minMachineMemoryGb),
        max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
        max_machine_memory_mb: optionalMemoryMb(values.maxMachineMemoryGb) ?? memoryMb,
      }
  }
}

export function machinePoolFormFromPool(pool: MachinePool): MachinePoolFormValues | null {
  if (!isMachinePoolProvider(pool.provider)) return null
  const provider = pool.provider
  const definition = machinePoolProviderDefinitions[provider]
  const options = pool.default_machine_provider_options
  const cpuValue =
    definition.resources.cpu === 'provider-resolved'
      ? pool.max_machine_cpu
      : pool.default_machine_cpu
  const memoryValue =
    definition.resources.memoryMb === 'provider-resolved'
      ? pool.max_machine_memory_mb
      : pool.default_machine_memory_mb
  return {
    name: pool.name,
    description: pool.description,
    provider,
    workspace: stringValue(pool.provider_config.workspace),
    image: stringValue(options[definition.resource.key]),
    location: stringValue(options[definition.location.key]),
    startupScript: stringValue(options.startup_script),
    cwd: pool.default_cwd,
    envRows: envRowsFromRecord(pool.default_machine_env),
    secretEnvRows: secretEnvRowsFromRecord(pool.default_machine_secret_env),
    cpu: numberDraft(cpuValue) || machinePoolFormDefaults.cpu,
    memoryGb: memoryGbDraft(memoryValue) || machinePoolFormDefaults.memoryGb,
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
  const editableOptionKeys = new Set([
    definition.resource.key,
    definition.location.key,
    'startup_script',
  ])
  const defaultMachineProviderOptions = {
    ...Object.fromEntries(
      Object.entries(pool.default_machine_provider_options).filter(
        ([key]) => !editableOptionKeys.has(key),
      ),
    ),
    [definition.resource.key]: values.image.trim(),
    [definition.location.key]: values.location.trim(),
    ...(values.startupScript.trim() === '' ? {} : { startup_script: values.startupScript }),
  }
  const cpu = Number(values.cpu)
  const originalMemoryMb =
    definition.resources.memoryMb === 'provider-resolved'
      ? pool.max_machine_memory_mb
      : pool.default_machine_memory_mb
  const memoryMb = memoryMbFromDraft(values.memoryGb, originalMemoryMb)
  const maxMachines = Number(values.maxMachines)
  const common = {
    name: values.name.trim(),
    description: values.description.trim(),
    default_machine_env: envFromRows(values.envRows) ?? {},
    default_machine_secret_env: secretEnvFromRows(values.secretEnvRows) ?? {},
    default_machine_provider_options: defaultMachineProviderOptions,
    default_cwd: values.cwd.trim(),
    provider_auth_secret_id: values.secretId,
    runtime_protection_enabled: values.runtimeProtectionEnabled,
    max_total_machines: maxMachines,
  }
  switch (values.provider) {
    case 'unikraft':
      return {
        ...common,
        default_machine_cpu: cpu,
        default_machine_memory_mb: memoryMb,
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
        provider_config: { ...pool.provider_config, workspace: values.workspace.trim() },
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
  const definition = machinePoolProviderDefinitions[values.provider]
  const cpu = Number(values.cpu)
  const originalMemoryMb =
    definition.resources.memoryMb === 'provider-resolved'
      ? pool.max_machine_memory_mb
      : pool.default_machine_memory_mb
  const memoryMb = memoryMbFromDraft(values.memoryGb, originalMemoryMb)
  const common = {
    default_machine_env: envFromRows(values.envRows) ?? {},
    default_machine_secret_env: secretEnvFromRows(values.secretEnvRows) ?? {},
  }
  switch (values.provider) {
    case 'unikraft':
      return {
        ...common,
        default_machine_cpu: cpu,
        default_machine_memory_mb: memoryMb,
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
        min_machine_cpu: optionalIntOrNull(values.minMachineCpu),
        min_machine_memory_mb: optionalMemoryMbOrNull(
          values.minMachineMemoryGb,
          pool.min_machine_memory_mb,
        ),
        max_machine_cpu: cpu,
        max_machine_memory_mb: memoryMb,
      }
  }
}

function envRowsFromRecord(values: Record<string, string>): EnvOverlayRow[] {
  return Object.entries(values)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => ({ ...newEnvOverlayRow(), key, value }))
}

function secretEnvRowsFromRecord(values: Record<string, string>): SecretEnvOverlayRow[] {
  return Object.entries(values)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, secretId]) => ({ ...newSecretEnvOverlayRow(), key, secretId }))
}

function stringValue(value: unknown) {
  return typeof value === 'string' ? value : ''
}

function memoryMbFromDraft(value: string, originalMemoryMb: number | null) {
  return originalMemoryMb === null
    ? memoryGbToMb(value)
    : memoryGbToMbPreservingOriginal(value, originalMemoryMb)
}

function optionalMemoryMb(value: string) {
  return value.trim() === '' ? undefined : memoryGbToMb(value)
}

function optionalMemoryMbPreservingOriginal(value: string, originalMemoryMb: number | null) {
  return value.trim() === '' ? undefined : memoryMbFromDraft(value, originalMemoryMb)
}

function optionalMemoryMbOrNull(value: string, originalMemoryMb: number | null) {
  return optionalMemoryMbPreservingOriginal(value, originalMemoryMb) ?? null
}
