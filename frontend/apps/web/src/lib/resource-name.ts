import { zResourceName } from '@omnara/sdk/zod'

export const resourceNameMaxCodePoints = 64

// HTML maxlength counts UTF-16 code units, so allow two per code point.
export const resourceNameInputMaxLength = 2 * resourceNameMaxCodePoints

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

export function resourceNameSuggestion(preferredValues: readonly string[], fallback: string) {
  fallback = normalizeResourceName(fallback)
  if (!resourceNameValid(fallback)) throw new Error('resource name suggestion fallback is invalid')

  for (const value of preferredValues) {
    const normalized = normalizeResourceName(value)
    if (resourceNameValid(normalized)) return normalized
  }
  if (preferredValues.length === 0) return fallback

  for (let index = preferredValues.length - 1; index >= 0; index -= 1) {
    const value = preferredValues[index]
    if (value === undefined) continue
    const shortened = hashedResourceNameSuggestion(value, value)
    if (shortened !== undefined) return shortened
  }
  const identity = preferredValues.at(-1) ?? fallback
  const safeFallback = hashedResourceNameSuggestion(fallback, identity)
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
