import { resourceNameValid } from '@/lib/resource-name'

import {
  derivedMemoryTotalCapPlaceholder,
  derivedTotalCapPlaceholder,
  machinePoolCapacityValid,
  type MachinePoolFormValues,
} from './MachinePoolDialogState'
import { machinePoolProviderDefinitions } from './machinePoolProviders'

export const machinePoolCreateSteps = ['provider', 'image', 'capacity', 'environment'] as const

export type MachinePoolCreateStep = (typeof machinePoolCreateSteps)[number]

/**
 * Whether a create step's own fields are complete. The environment step has only optional
 * fields; creating the pool still requires the whole form to be valid.
 */
export function machinePoolCreateStepValid(
  step: MachinePoolCreateStep,
  values: MachinePoolFormValues,
) {
  const provider = machinePoolProviderDefinitions[values.provider]
  switch (step) {
    case 'provider':
      return resourceNameValid(values.name) && values.secretId !== ''
    case 'image':
      return (
        (provider.resource.optional === true || values.image.trim() !== '') &&
        (!provider.scope?.required || values.providerScope.trim() !== '')
      )
    case 'capacity':
      return (
        (!provider.location?.required || values.location.trim() !== '') &&
        machinePoolCapacityValid(values)
      )
    case 'environment':
      return true
  }
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
