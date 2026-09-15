const dateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'short',
})

export function formatDateTime(value: string | null | undefined) {
  if (!value) return undefined
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return undefined
  return dateTimeFormatter.format(date)
}

const countFormatter = new Intl.NumberFormat(undefined)

export function formatCount(value: number) {
  return countFormatter.format(value)
}

const usdFormatter = new Intl.NumberFormat(undefined, {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 4,
})

export function formatUsd(decimal: string) {
  const value = Number(decimal)
  if (!Number.isFinite(value)) return decimal
  return usdFormatter.format(value)
}

const compactCountFormatter = new Intl.NumberFormat(undefined, {
  notation: 'compact',
  maximumFractionDigits: 1,
})

export function formatCompactCount(value: number) {
  return compactCountFormatter.format(value)
}
