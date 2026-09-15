import { memoryGbToMb, memoryGbToMbPreservingOriginal } from '@/lib/machine-memory'

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
