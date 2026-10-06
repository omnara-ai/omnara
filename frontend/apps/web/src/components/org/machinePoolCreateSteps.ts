import {
  derivedMemoryTotalCapPlaceholder,
  derivedTotalCapPlaceholder,
  type MachinePoolFormValues,
} from './MachinePoolDialogState'
import { machinePoolProviderDefinitions } from './machinePoolProviders'
import {
  type MachinePoolFieldGroup,
  machinePoolFieldGroups,
  machinePoolFieldGroupValid,
} from './machinePoolValidation'

/** Create walks through the form one field group per step. */
export const machinePoolCreateSteps = machinePoolFieldGroups

export type MachinePoolCreateStep = MachinePoolFieldGroup

/**
 * Whether a create step's own fields are valid: the same per-field validation as the whole
 * form, limited to the step's group, so every valid step together means Create is enabled.
 */
export function machinePoolCreateStepValid(
  step: MachinePoolCreateStep,
  values: MachinePoolFormValues,
) {
  return machinePoolFieldGroupValid(step, values, 'create')
}

/** The size of each machine, e.g. "2 vCPU · 4 GB"; undefined while a size input is invalid. */
export function machinePoolMachineSizeLabel(values: MachinePoolFormValues) {
  const { cpu, memoryMb } = machinePoolProviderDefinitions[values.provider].resources
  const parts: string[] = []
  if (cpu !== 'unsupported') {
    if (derivedTotalCapPlaceholder(values.cpu, '1') === undefined) return undefined
    parts.push(`${Number(values.cpu)} vCPU`)
  }
  if (memoryMb !== 'unsupported') {
    const memoryGb = derivedMemoryTotalCapPlaceholder(values.memoryGb, '1')
    if (memoryGb === undefined) return undefined
    parts.push(`${memoryGb} GB`)
  }
  return parts.join(' · ')
}

/** Pool-wide totals, e.g. "6 vCPU · 3 GB total", honoring any total caps set in Advanced. */
export function machinePoolTotalLabel(values: MachinePoolFormValues) {
  const { cpu, memoryMb } = machinePoolProviderDefinitions[values.provider].resources
  const parts: string[] = []
  if (cpu !== 'unsupported') {
    const total =
      values.maxTotalCpu.trim() || derivedTotalCapPlaceholder(values.cpu, values.maxMachines)
    if (total) parts.push(`${total} vCPU`)
  }
  if (memoryMb !== 'unsupported') {
    const total =
      values.maxTotalMemoryGb.trim() ||
      derivedMemoryTotalCapPlaceholder(values.memoryGb, values.maxMachines)
    if (total) parts.push(`${total} GB`)
  }
  return parts.length > 0 ? `${parts.join(' · ')} total` : undefined
}
