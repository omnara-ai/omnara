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

const usdPerMillionFormatter = new Intl.NumberFormat(undefined, {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 3,
})

export function formatUsdPerMillion(decimal: string) {
  const value = Number(decimal)
  if (!Number.isFinite(value)) return decimal
  return usdPerMillionFormatter.format(value)
}

const timeFormatter = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
})

export function formatTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return undefined
  return timeFormatter.format(date)
}

const timeAgoFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
})

export function formatTimeAgo(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  const seconds = Math.round((Date.now() - date.getTime()) / 1000)
  if (seconds < 60) return 'Just now'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.round(hours / 24)
  if (days < 7) return `${days}d ago`
  return timeAgoFormatter.format(date)
}
