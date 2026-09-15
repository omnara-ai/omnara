import { memoryGbDraft, memoryGbDraftValid, memoryGbToMb } from '@/lib/machine-memory'

import {
  type MachinePoolProvider,
  machinePoolProviderDefinitions,
  type MachineSizeClass,
} from './machinePoolProviders'

/** Picker value that leaves the machine size to the provider. */
export const providerDefaultSizeValue = 'provider-default'

export interface MachineSizeDrafts {
  cpu: string
  memoryGb: string
}

export function machineSizeClassValue(size: MachineSizeClass) {
  return `${size.cpu}x${size.memoryMb}`
}

export function machineSizeClassLabel(size: MachineSizeClass) {
  return `${size.cpu} vCPU · ${memoryGbDraft(size.memoryMb)} GB`
}

/**
 * The picker value that the form's CPU and memory drafts name: an offered
 * size, the provider default when both are empty and that is allowed, or ''
 * when they name nothing the provider offers.
 */
export function machineSizeSelection(provider: MachinePoolProvider, drafts: MachineSizeDrafts) {
  const sizes = machinePoolProviderDefinitions[provider].sizes
  if (!sizes) return ''
  if (drafts.cpu.trim() === '' && drafts.memoryGb.trim() === '') {
    return sizes.providerDefault ? providerDefaultSizeValue : ''
  }
  const match = sizes.classes.find((size) => matchesSizeClass(size, drafts))
  return match ? machineSizeClassValue(match) : ''
}

/** The CPU and memory drafts for a picker value; anything unknown clears both. */
export function machineSizeDrafts(
  provider: MachinePoolProvider,
  selection: string,
): MachineSizeDrafts {
  const sizes = machinePoolProviderDefinitions[provider].sizes
  const match = sizes?.classes.find((size) => machineSizeClassValue(size) === selection)
  return match ? sizeClassDrafts(match) : { cpu: '', memoryGb: '' }
}

/**
 * Whether the drafts are acceptable for the provider: any positive numbers
 * for a provider with free sizing, otherwise an offered size or, when the
 * provider allows it, no size at all.
 */
export function machineSizeValid(provider: MachinePoolProvider, drafts: MachineSizeDrafts) {
  if (!machinePoolProviderDefinitions[provider].sizes) return true
  return machineSizeSelection(provider, drafts) !== ''
}

/** True when the form leaves the machine size to the provider. */
export function machineSizeOmitted(provider: MachinePoolProvider, drafts: MachineSizeDrafts) {
  return machineSizeSelection(provider, drafts) === providerDefaultSizeValue
}

/**
 * The drafts that size a machine: the form's own CPU and memory, or the
 * per-machine caps while the size is left to the provider.
 */
export function effectiveMachineSizeDrafts(
  provider: MachinePoolProvider,
  values: MachineSizeDrafts & { maxMachineCpu: string; maxMachineMemoryGb: string },
): MachineSizeDrafts {
  return machineSizeOmitted(provider, values)
    ? { cpu: values.maxMachineCpu, memoryGb: values.maxMachineMemoryGb }
    : { cpu: values.cpu, memoryGb: values.memoryGb }
}

/** The smallest offered size, for a form that switches to a provider with fixed shapes. */
export function smallestMachineSizeDrafts(provider: MachinePoolProvider): MachineSizeDrafts | null {
  const first = machinePoolProviderDefinitions[provider].sizes?.classes[0]
  return first ? sizeClassDrafts(first) : null
}

function sizeClassDrafts(size: MachineSizeClass): MachineSizeDrafts {
  return { cpu: String(size.cpu), memoryGb: memoryGbDraft(size.memoryMb) }
}

function matchesSizeClass(size: MachineSizeClass, drafts: MachineSizeDrafts) {
  return (
    drafts.cpu.trim() === String(size.cpu) &&
    memoryGbDraftValid(drafts.memoryGb) &&
    memoryGbToMb(drafts.memoryGb) === size.memoryMb
  )
}
