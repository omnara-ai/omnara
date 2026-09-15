import {
  type EnvOverlayRow,
  newEnvOverlayRow,
  newSecretEnvOverlayRow,
  type SecretEnvOverlayRow,
} from '@/components/machines/machineOverrides'
import { memoryGbToMb, memoryGbToMbPreservingOriginal } from '@/lib/machine-memory'

export function envRowsFromRecord(values: Record<string, string>): EnvOverlayRow[] {
  return Object.entries(values)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => ({ ...newEnvOverlayRow(), key, value }))
}

export function secretEnvRowsFromRecord(values: Record<string, string>): SecretEnvOverlayRow[] {
  return Object.entries(values)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, secretId]) => ({ ...newSecretEnvOverlayRow(), key, secretId }))
}

export function memoryMbFromDraft(value: string, originalMemoryMb: number | null) {
  return originalMemoryMb === null
    ? memoryGbToMb(value)
    : memoryGbToMbPreservingOriginal(value, originalMemoryMb)
}

export function optionalMemoryMb(value: string) {
  return value.trim() === '' ? undefined : memoryGbToMb(value)
}

export function optionalMemoryMbPreservingOriginal(value: string, originalMemoryMb: number | null) {
  return value.trim() === '' ? undefined : memoryMbFromDraft(value, originalMemoryMb)
}

export function optionalMemoryMbOrNull(value: string, originalMemoryMb: number | null) {
  return optionalMemoryMbPreservingOriginal(value, originalMemoryMb) ?? null
}
