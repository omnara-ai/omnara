import { zResourceName } from '@omnara/sdk/zod'

export const resourceNameMaxCodePoints = 64

export function normalizeResourceName(value: string) {
  return value.normalize('NFC')
}

export function resourceNameError(value: string, fieldLabel = 'Name'): string | undefined {
  const result = zResourceName.safeParse(value)
  if (result.success) return undefined
  if (value === '') return `${fieldLabel} is required.`

  const message = result.error.issues[0]?.message.replace(/^Resource name/u, fieldLabel)
  if (message === undefined) return `${fieldLabel} is invalid.`
  return message.endsWith('.') ? message : `${message}.`
}

export function resourceNameValid(value: string) {
  return zResourceName.safeParse(value).success
}

export function resourceNameSuggestion(value: string, fallback: string) {
  value = normalizeResourceName(value)
  fallback = normalizeResourceName(fallback)
  if (!resourceNameValid(fallback)) throw new Error('resource name suggestion fallback is invalid')

  if (resourceNameValid(value)) return value
  if (value === '') return fallback
  const shortened = hashedResourceNameSuggestion(value, value)
  if (shortened !== undefined) return shortened
  const safeFallback = hashedResourceNameSuggestion(fallback, value)
  if (safeFallback === undefined)
    throw new Error('could not generate a safe resource name suggestion')
  return safeFallback
}

function hashedResourceNameSuggestion(prefixSource: string, identitySource: string) {
  prefixSource = normalizeResourceName(prefixSource)
  identitySource = normalizeResourceName(identitySource)
  const suffix = `-${resourceNameHash(identitySource)}`
  const prefix = Array.from(prefixSource)
    .slice(0, resourceNameMaxCodePoints - Array.from(suffix).length)
    .join('')
    .replace(/ +$/u, '')
  const suggestion = prefix + suffix
  return resourceNameValid(suggestion) ? suggestion : undefined
}

function resourceNameHash(value: string) {
  let hash = 14_695_981_039_346_656_037n
  for (const byte of new TextEncoder().encode(value)) {
    hash = BigInt.asUintN(64, (hash ^ BigInt(byte)) * 1_099_511_628_211n)
  }
  return hash.toString(16).padStart(16, '0')
}
