const memoryMbPerGb = 1024
const maxInt32 = 2_147_483_647

const memoryGbFormatter = new Intl.NumberFormat(undefined, {
  maximumFractionDigits: 2,
})

function roundedMemoryGb(memoryMb: number) {
  return Math.round((memoryMb / memoryMbPerGb) * 100) / 100
}

export function formatMemoryGb(memoryMb: number | null | undefined) {
  if (memoryMb == null) return undefined
  if (memoryMb > 0 && memoryMb / memoryMbPerGb < 0.01) return '<0.01 GB'
  return `${memoryGbFormatter.format(memoryMb / memoryMbPerGb)} GB`
}

export function memoryGbDraft(memoryMb: number | null | undefined) {
  if (memoryMb == null) return ''
  const rounded = roundedMemoryGb(memoryMb)
  const roundedMemoryMb = Math.round(rounded * memoryMbPerGb)
  if ((memoryMb > 0 && rounded === 0) || roundedMemoryMb > maxInt32) {
    return String(memoryMb / memoryMbPerGb)
  }
  return String(rounded)
}

export function memoryGbDraftValid(
  value: string,
  { optional = false, allowZero = false }: { optional?: boolean; allowZero?: boolean } = {},
) {
  if (value.trim() === '') return optional
  const memoryGb = Number(value)
  if (!Number.isFinite(memoryGb) || (allowZero ? memoryGb < 0 : memoryGb <= 0)) return false
  const memoryMb = Math.round(memoryGb * memoryMbPerGb)
  return (memoryGb === 0 || memoryMb > 0) && memoryMb <= maxInt32
}

export function memoryGbToMb(value: string) {
  return Math.round(Number(value) * memoryMbPerGb)
}

export function memoryGbToMbPreservingOriginal(value: string, originalMemoryMb: number) {
  return value === memoryGbDraft(originalMemoryMb) ? originalMemoryMb : memoryGbToMb(value)
}
