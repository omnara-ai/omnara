import {
  type MachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'

export interface EnvOverlayRow {
  id: string
  key: string
  value: string
}

export interface SecretEnvOverlayRow {
  id: string
  key: string
  secretId: string
}

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

export function newEnvOverlayRow(): EnvOverlayRow {
  return { id: crypto.randomUUID(), key: '', value: '' }
}

export function newSecretEnvOverlayRow(): SecretEnvOverlayRow {
  return { id: crypto.randomUUID(), key: '', secretId: '' }
}

function overlayKeysValid(rows: { key: string }[]) {
  const keys = rows.map((row) => row.key.trim())
  return keys.every((key) => key !== '') && new Set(keys).size === keys.length
}

export function envOverlayRowsValid(rows: EnvOverlayRow[]) {
  return overlayKeysValid(rows)
}

export function secretEnvOverlayRowsValid(rows: SecretEnvOverlayRow[]) {
  return overlayKeysValid(rows) && rows.every((row) => row.secretId !== '')
}

export function envFromRows(rows: EnvOverlayRow[]): Record<string, string> | undefined {
  if (rows.length === 0) return undefined
  return Object.fromEntries(rows.map((row) => [row.key.trim(), row.value]))
}

export function secretEnvFromRows(rows: SecretEnvOverlayRow[]): Record<string, string> | undefined {
  if (rows.length === 0) return undefined
  return Object.fromEntries(rows.map((row) => [row.key.trim(), row.secretId]))
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

export function optionalInt(value: string): number | undefined {
  return value.trim() === '' ? undefined : Number(value)
}

export function stringOrUndefined(value: string): string | undefined {
  return value.trim() === '' ? undefined : value.trim()
}
