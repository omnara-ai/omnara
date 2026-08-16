import type { CreateMachinePoolRequest } from '@omnara/sdk'

import {
  envFromRows,
  type EnvOverlayRow,
  envOverlayRowsValid,
  optionalInt,
  optionalNonNegativeInt32Valid,
  optionalPositiveInt32Valid,
  secretEnvFromRows,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
  stringOrUndefined,
} from '@/components/machines/machineOverrides'
import { memoryGbDraft, memoryGbDraftValid, memoryGbToMb } from '@/lib/machine-memory'
import { resourceNameValid } from '@/lib/resource-name'

import { type MachinePoolProvider, machinePoolProviderDefinitions } from './machinePoolProviders'

export const machinePoolProviders = Object.entries(machinePoolProviderDefinitions).map(
  ([value, definition]) => ({ value, label: definition.label }),
)

export function machinePoolProviderLabel(provider: string) {
  return machinePoolProviders.find((option) => option.value === provider)?.label ?? provider
}

export interface MachinePoolFormValues {
  name: string
  provider: MachinePoolProvider
  workspace: string
  image: string
  location: string
  startupScript: string
  cwd: string
  envRows: EnvOverlayRow[]
  secretEnvRows: SecretEnvOverlayRow[]
  /** Positive integer as entered in the number input. */
  cpu: string
  memoryGb: string
  /** Positive integer as entered in the number input. */
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

export const machinePoolFormDefaults: MachinePoolFormValues = {
  name: '',
  provider: 'unikraft',
  workspace: '',
  image: '',
  location: 'sfo',
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
  if (!positiveInt32(perMachine) || !positiveInt32(maxMachines)) return undefined
  if (!aggregateFitsInt32(perMachine, maxMachines)) return undefined
  return String(Number(perMachine) * Number(maxMachines))
}

export function derivedMemoryTotalCapPlaceholder(perMachineGb: string, maxMachines: string) {
  if (!memoryGbDraftValid(perMachineGb) || !positiveInt32(maxMachines)) return undefined
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

export function machinePoolFormValid(values: MachinePoolFormValues) {
  const provider = machinePoolProviderDefinitions[values.provider]
  const maxMachinesValid = positiveInt32(values.maxMachines)
  const cpuValid =
    provider.resources.cpu === 'unsupported' ||
    (positiveInt32(values.cpu) &&
      maxMachinesValid &&
      (values.maxTotalCpu.trim() !== '' || aggregateFitsInt32(values.cpu, values.maxMachines)))
  const memoryValid =
    provider.resources.memoryMb === 'unsupported' ||
    (memoryGbDraftValid(values.memoryGb) &&
      maxMachinesValid &&
      (values.maxTotalMemoryGb.trim() !== '' ||
        memoryAggregateFitsInt32(values.memoryGb, values.maxMachines)))
  return (
    resourceNameValid(values.name) &&
    values.image.trim() !== '' &&
    values.location.trim() !== '' &&
    (!provider.requiresWorkspace || values.workspace.trim() !== '') &&
    values.secretId !== '' &&
    maxMachinesValid &&
    cpuValid &&
    memoryValid &&
    envOverlayRowsValid(values.envRows) &&
    secretEnvOverlayRowsValid(values.secretEnvRows) &&
    optionalPositiveInt32Valid(values.maxMachineCpu) &&
    memoryGbDraftValid(values.maxMachineMemoryGb, { optional: true }) &&
    [values.maxTotalCpu, values.minMachineCpu].every(optionalNonNegativeInt32Valid) &&
    memoryGbDraftValid(values.maxTotalMemoryGb, { optional: true, allowZero: true }) &&
    memoryGbDraftValid(values.minMachineMemoryGb, { optional: true, allowZero: true })
  )
}

export function machinePoolCreateRequest(values: MachinePoolFormValues): CreateMachinePoolRequest {
  const cpu = Number(values.cpu)
  const memoryMb = memoryGbToMb(values.memoryGb)
  const maxMachines = Number(values.maxMachines)
  const common = {
    name: values.name,
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

function optionalMemoryMb(value: string) {
  return value.trim() === '' ? undefined : memoryGbToMb(value)
}
