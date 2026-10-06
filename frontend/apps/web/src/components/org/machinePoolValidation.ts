import { secretRowsValid, textRowsValid } from '@/components/key-value/keyValueRows'
import {
  optionalNonNegativeInt32Valid,
  optionalPoolIdleDeletionMinutesValid,
  optionalPositiveInt32Valid,
} from '@/components/machines/machineOverrides'
import { memoryGbDraftValid, memoryGbToMb } from '@/lib/machine-memory'
import { resourceNameError, resourceNameValid } from '@/lib/resource-name'

import type { MachinePoolFormMode, MachinePoolFormValues } from './MachinePoolDialogState'
import { machinePoolProviderDefinitions } from './machinePoolProviders'

const maxInt32 = 2_147_483_647

export function positiveInt32(value: string) {
  return value.trim() !== '' && optionalPositiveInt32Valid(value)
}

export function nonNegativeInt32(value: string) {
  return value.trim() !== '' && optionalNonNegativeInt32Valid(value)
}

export function aggregateFitsInt32(perMachine: string, maxMachines: string) {
  const perMachineValue = Number(perMachine)
  const maxMachinesValue = Number(maxMachines)
  return perMachineValue <= Math.floor(maxInt32 / maxMachinesValue)
}

export function memoryAggregateFitsInt32(perMachineGb: string, maxMachines: string) {
  return memoryGbToMb(perMachineGb) <= Math.floor(maxInt32 / Number(maxMachines))
}

/**
 * The form's sections. The create dialog shows one per step and the edit dialog shows them
 * all, so both render and validate the same fields.
 */
export const machinePoolFieldGroups = ['provider', 'image', 'capacity', 'environment'] as const

export type MachinePoolFieldGroup = (typeof machinePoolFieldGroups)[number]

/** Every form value outside the dialog footer's project sharing, which can't be invalid. */
export type MachinePoolFieldKey = Exclude<keyof MachinePoolFormValues, 'projectGrantIds'>

/** The section each field renders and is validated in; the Record type keeps it exhaustive. */
export const machinePoolFieldGroup: Record<MachinePoolFieldKey, MachinePoolFieldGroup> = {
  name: 'provider',
  provider: 'provider',
  description: 'provider',
  secretId: 'provider',
  image: 'image',
  providerScope: 'image',
  location: 'capacity',
  cpu: 'capacity',
  memoryGb: 'capacity',
  maxMachines: 'capacity',
  maxTotalCpu: 'capacity',
  maxTotalMemoryGb: 'capacity',
  minMachineCpu: 'capacity',
  minMachineMemoryGb: 'capacity',
  maxMachineCpu: 'capacity',
  maxMachineMemoryGb: 'capacity',
  deleteAfterIdleMinutes: 'capacity',
  runtimeProtectionEnabled: 'capacity',
  startupScript: 'environment',
  cwd: 'environment',
  envRows: 'environment',
  secretEnvRows: 'environment',
}

const fieldGroupByKey = new Map<string, MachinePoolFieldGroup>(
  Object.entries(machinePoolFieldGroup),
)

export type MachinePoolFieldErrors = Partial<Record<MachinePoolFieldKey, string>>

const tooLargeForMachineCount = 'Too large for this many machines; lower it or set a max total.'

/**
 * Why each invalid field is invalid, for the fields the mode lets the user edit. The single
 * source of form validity: steps, Save, and Create all derive from it.
 */
export function machinePoolFieldErrors(
  values: MachinePoolFormValues,
  mode: MachinePoolFormMode = 'create',
): MachinePoolFieldErrors {
  const provider = machinePoolProviderDefinitions[values.provider]
  const clusterEdit = mode === 'cluster-edit'
  const errors: MachinePoolFieldErrors = {}
  if (!clusterEdit) {
    if (!resourceNameValid(values.name)) {
      errors.name = resourceNameError(values.name) ?? 'Name is invalid.'
    }
    if (values.secretId === '') errors.secretId = 'Choose a credential.'
    if (provider.resource.optional !== true && values.image.trim() === '') {
      errors.image = `${provider.resource.label} is required.`
    }
    if (provider.scope?.required && values.providerScope.trim() === '') {
      errors.providerScope = `${provider.scope.label} is required.`
    }
    if (provider.location?.required && values.location.trim() === '') {
      errors.location = `${provider.location.label} is required.`
    }
    if (!nonNegativeInt32(values.maxMachines)) {
      errors.maxMachines = 'Enter a whole number of 0 or more.'
    }
  }
  // Without an explicit total cap, the derived total (size × max machines) must fit too.
  const checkDerivedTotals = !clusterEdit && errors.maxMachines === undefined
  if (provider.resources.cpu !== 'unsupported') {
    if (!positiveInt32(values.cpu)) {
      errors.cpu = 'Enter a whole number of 1 or more.'
    } else if (
      checkDerivedTotals &&
      values.maxTotalCpu.trim() === '' &&
      !aggregateFitsInt32(values.cpu, values.maxMachines)
    ) {
      errors.cpu = tooLargeForMachineCount
    }
  }
  if (provider.resources.memoryMb !== 'unsupported') {
    if (!memoryGbDraftValid(values.memoryGb)) {
      errors.memoryGb = 'Enter a size greater than 0 GB.'
    } else if (
      checkDerivedTotals &&
      values.maxTotalMemoryGb.trim() === '' &&
      !memoryAggregateFitsInt32(values.memoryGb, values.maxMachines)
    ) {
      errors.memoryGb = tooLargeForMachineCount
    }
  }
  if (!clusterEdit && !optionalNonNegativeInt32Valid(values.maxTotalCpu)) {
    errors.maxTotalCpu = 'Enter a whole number of 0 or more, or leave it empty.'
  }
  if (
    !clusterEdit &&
    !memoryGbDraftValid(values.maxTotalMemoryGb, { optional: true, allowZero: true })
  ) {
    errors.maxTotalMemoryGb = 'Enter 0 GB or more, or leave it empty.'
  }
  if (!optionalNonNegativeInt32Valid(values.minMachineCpu)) {
    errors.minMachineCpu = 'Enter a whole number of 0 or more, or leave it empty.'
  }
  if (!memoryGbDraftValid(values.minMachineMemoryGb, { optional: true, allowZero: true })) {
    errors.minMachineMemoryGb = 'Enter 0 GB or more, or leave it empty.'
  }
  if (!optionalPositiveInt32Valid(values.maxMachineCpu)) {
    errors.maxMachineCpu = 'Enter a whole number of 1 or more, or leave it empty.'
  }
  if (!memoryGbDraftValid(values.maxMachineMemoryGb, { optional: true })) {
    errors.maxMachineMemoryGb = 'Enter a size greater than 0 GB, or leave it empty.'
  }
  if (!optionalPoolIdleDeletionMinutesValid(values.deleteAfterIdleMinutes)) {
    errors.deleteAfterIdleMinutes = 'Enter a whole number of at least 5, or leave it empty.'
  }
  if (!textRowsValid(values.envRows)) {
    errors.envRows = 'Each variable needs a unique name.'
  }
  if (!secretRowsValid(values.secretEnvRows)) {
    errors.secretEnvRows = 'Each secret variable needs a unique name and a secret.'
  }
  return errors
}

/** The error to show under a field: empty fields only block progress, they aren't flagged. */
export function shownFieldError(value: string, error: string | undefined) {
  return value.trim() === '' ? undefined : error
}

/** Whether every field in one section is valid. */
export function machinePoolFieldGroupValid(
  group: MachinePoolFieldGroup,
  values: MachinePoolFormValues,
  mode: MachinePoolFormMode = 'create',
) {
  return Object.keys(machinePoolFieldErrors(values, mode)).every(
    (key) => fieldGroupByKey.get(key) !== group,
  )
}

/** Whether every section is valid, i.e. the pool can be saved. */
export function machinePoolFormValid(
  values: MachinePoolFormValues,
  mode: MachinePoolFormMode = 'create',
) {
  return machinePoolFieldGroups.every((group) => machinePoolFieldGroupValid(group, values, mode))
}
