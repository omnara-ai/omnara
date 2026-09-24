import {
  type MachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'

export interface ProviderOptionsDraft {
  resource: string
  location: string
  startupScript: string
}

export const emptyProviderOptions: ProviderOptionsDraft = {
  resource: '',
  location: '',
  startupScript: '',
}

export function providerOptionsOverlay(
  provider: MachinePoolProvider,
  draft: ProviderOptionsDraft,
  clusterManaged = false,
): Record<string, string> | undefined {
  const definition = machinePoolProviderDefinitions[provider]
  const overlay: Record<string, string> = {}
  if (!clusterManaged) {
    if (draft.resource.trim() !== '') overlay[definition.resource.key] = draft.resource.trim()
    if (draft.location.trim() !== '') overlay[definition.location.key] = draft.location.trim()
  }
  if (draft.startupScript.trim() !== '') overlay.startup_script = draft.startupScript
  return Object.keys(overlay).length > 0 ? overlay : undefined
}

const maxInt32 = 2_147_483_647

export function optionalPositiveInt32Valid(value: string) {
  if (value.trim() === '') return true
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 && parsed <= maxInt32
}

export function optionalNonNegativeInt32Valid(value: string) {
  if (value.trim() === '') return true
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed >= 0 && parsed <= maxInt32
}

export function idleDeletionMinutesValid(value: number) {
  return Number.isInteger(value) && (value === 0 || (value >= 5 && value <= maxInt32))
}

export function optionalIdleDeletionMinutesValid(value: string) {
  if (value.trim() === '') return true
  return idleDeletionMinutesValid(Number(value))
}

export function optionalPoolIdleDeletionMinutesValid(value: string) {
  if (value.trim() === '') return true
  const parsed = Number(value)
  return parsed !== 0 && idleDeletionMinutesValid(parsed)
}

export function optionalInt(value: string): number | undefined {
  return value.trim() === '' ? undefined : Number(value)
}

export function optionalIntOrNull(value: string): number | null {
  return value.trim() === '' ? null : Number(value)
}

export function numberDraft(value: number | null | undefined): string {
  return value == null ? '' : String(value)
}

export function stringOrUndefined(value: string): string | undefined {
  return value.trim() === '' ? undefined : value.trim()
}
