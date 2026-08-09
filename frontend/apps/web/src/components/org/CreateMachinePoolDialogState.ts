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
  /** Positive integer as entered in the number input. */
  memoryMb: string
  /** Positive integer as entered in the number input. */
  maxMachines: string
  /** Optional caps; empty derives from machine size × max machines. */
  maxTotalCpu: string
  maxTotalMemoryMb: string
  maxMachineCpu: string
  maxMachineMemoryMb: string
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
  memoryMb: '1024',
  maxMachines: '3',
  maxTotalCpu: '',
  maxTotalMemoryMb: '',
  maxMachineCpu: '',
  maxMachineMemoryMb: '',
  secretId: '',
  projectGrantIds: [],
  runtimeProtectionEnabled: true,
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

/** Placeholder for an aggregate cap input: machine size × max machines. */
export function derivedTotalCapPlaceholder(perMachine: string, maxMachines: string) {
  if (!positiveInt32(perMachine) || !positiveInt32(maxMachines)) return undefined
  if (!aggregateFitsInt32(perMachine, maxMachines)) return undefined
  return String(Number(perMachine) * Number(maxMachines))
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
    memoryMb:
      currentDefinition.resources.memoryMb === nextDefinition.resources.memoryMb
        ? values.memoryMb
        : machinePoolFormDefaults.memoryMb,
    maxTotalCpu: '',
    maxTotalMemoryMb: '',
    maxMachineCpu: '',
    maxMachineMemoryMb: '',
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
    (positiveInt32(values.memoryMb) &&
      maxMachinesValid &&
      (values.maxTotalMemoryMb.trim() !== '' ||
        aggregateFitsInt32(values.memoryMb, values.maxMachines)))
  return (
    values.name.trim() !== '' &&
    values.image.trim() !== '' &&
    values.location.trim() !== '' &&
    (!provider.requiresWorkspace || values.workspace.trim() !== '') &&
    values.secretId !== '' &&
    maxMachinesValid &&
    cpuValid &&
    memoryValid &&
    envOverlayRowsValid(values.envRows) &&
    secretEnvOverlayRowsValid(values.secretEnvRows) &&
    [values.maxMachineCpu, values.maxMachineMemoryMb].every(optionalPositiveInt32Valid) &&
    [values.maxTotalCpu, values.maxTotalMemoryMb].every(optionalNonNegativeInt32Valid)
  )
}

export function machinePoolCreateRequest(values: MachinePoolFormValues): CreateMachinePoolRequest {
  const cpu = Number(values.cpu)
  const memoryMb = Number(values.memoryMb)
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
        max_total_memory_mb: optionalInt(values.maxTotalMemoryMb) ?? memoryMb * maxMachines,
        max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
        max_machine_memory_mb: optionalInt(values.maxMachineMemoryMb) ?? memoryMb,
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
        max_total_memory_mb: optionalInt(values.maxTotalMemoryMb) ?? memoryMb * maxMachines,
        max_machine_memory_mb: optionalInt(values.maxMachineMemoryMb) ?? memoryMb,
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
        max_total_memory_mb: optionalInt(values.maxTotalMemoryMb) ?? memoryMb * maxMachines,
        max_machine_cpu: optionalInt(values.maxMachineCpu) ?? cpu,
        max_machine_memory_mb: optionalInt(values.maxMachineMemoryMb) ?? memoryMb,
      }
  }
}
