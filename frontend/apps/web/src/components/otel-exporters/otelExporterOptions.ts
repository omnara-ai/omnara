import type { OtelExporterAuthKind, OtelExporterEncoding, OtelTraceVariant } from '@omnara/sdk'

import {
  type SecretRow,
  type TextRow,
  textRowsFromRecord,
} from '@/components/key-value/keyValueRows'

export const variants: OtelTraceVariant[] = ['otel', 'langfuse', 'braintrust', 'langsmith']
export const otelVariantLabels: Record<OtelTraceVariant, string> = {
  otel: 'OpenTelemetry',
  langfuse: 'Langfuse',
  braintrust: 'Braintrust',
  langsmith: 'LangSmith',
}

export const otelEncodingLabels: Record<OtelExporterEncoding, string> = {
  protobuf: 'Protobuf',
  json: 'JSON',
}

export const otelAuthKinds: OtelExporterAuthKind[] = [
  'none',
  'bearer_token',
  'api_key_header',
  'basic',
]

export const otelAuthKindLabels: Record<OtelExporterAuthKind, string> = {
  none: 'None',
  bearer_token: 'Bearer token',
  api_key_header: 'API key header',
  basic: 'Basic auth',
}

/** The encodings each variant's backend accepts, its default first. Mirrors the API's rule. */
export const variantEncodings: Record<OtelTraceVariant, OtelExporterEncoding[]> = {
  otel: ['protobuf', 'json'],
  langfuse: ['protobuf', 'json'],
  braintrust: ['protobuf'],
  langsmith: ['protobuf'],
}

/** The hosted traces endpoint of each backend, filled in when its variant is picked. */
export const variantEndpoints: Record<OtelTraceVariant, string> = {
  otel: '',
  langfuse: 'https://cloud.langfuse.com/api/public/otel/v1/traces',
  braintrust: 'https://api.braintrust.dev/otel/v1/traces',
  langsmith: 'https://api.smith.langchain.com/otel/v1/traces',
}

interface VariantPreset {
  authKind: OtelExporterAuthKind
  authHeaderName: string
  textHeaders: Record<string, string>
}

const variantPresets: Record<OtelTraceVariant, VariantPreset> = {
  otel: { authKind: 'none', authHeaderName: '', textHeaders: {} },
  langfuse: {
    authKind: 'basic',
    authHeaderName: '',
    textHeaders: { 'x-langfuse-ingestion-version': '4' },
  },
  braintrust: {
    authKind: 'bearer_token',
    authHeaderName: '',
    textHeaders: { 'x-bt-parent': 'project_id:' },
  },
  langsmith: {
    authKind: 'api_key_header',
    authHeaderName: 'x-api-key',
    textHeaders: {},
  },
}

/**
 * Braintrust files spans under the parent its x-bt-parent header names: either `kind:value` or
 * a `span.export()` slug, which has no colon. The preset fills in only the kind, and Braintrust
 * rejects every export until a value follows, so only that unfinished form is an error.
 */
export function braintrustParentError(variant: OtelTraceVariant, headerRows: TextRow[]) {
  if (variant !== 'braintrust') return undefined
  const incomplete = headerRows.some((row) => {
    if (row.key.trim().toLowerCase() !== 'x-bt-parent') return false
    const value = (row.value ?? '').trim()
    const separator = value.indexOf(':')
    if (separator < 0) return value === ''
    return separator === 0 || value.slice(separator + 1).trim() === ''
  })
  return incomplete
    ? 'Finish the x-bt-parent header with your Braintrust project, such as project_id:<id>.'
    : undefined
}

/**
 * The endpoint to show after switching variant: the new backend's own, unless the user has
 * typed one that is not just another backend's default.
 */
export function endpointForVariant(variant: OtelTraceVariant, current: string) {
  const untouched = current.trim() === '' || Object.values(variantEndpoints).includes(current)
  return untouched ? variantEndpoints[variant] : current
}

function urlOrigin(value: string): string {
  try {
    return new URL(value.trim()).origin
  } catch {
    return ''
  }
}

function matchesPresetTextHeaders(variant: OtelTraceVariant, textRows: TextRow[]) {
  const text = Object.entries(variantPresets[variant].textHeaders)
  return (
    textRows.length === text.length &&
    text.every(([key, value]) => textRows.some((row) => row.key === key && row.value === value))
  )
}

export interface VariantSwitchInput {
  nextVariant: OtelTraceVariant
  endpoint: string
  encoding: OtelExporterEncoding
  authKind: OtelExporterAuthKind
  authHeaderName: string
  authUsername: string
  credentialSecretId: string
  headerRows: TextRow[]
  secretHeaderRows: SecretRow[]
}

/**
 * Computes the next form state when switching backend variant. Whenever automatic endpoint
 * substitution changes the destination origin, credentials are cleared so secrets configured for
 * one tracing host cannot be forwarded to another.
 */
export function applyVariantSwitch(input: VariantSwitchInput) {
  const nextEndpoint = endpointForVariant(input.nextVariant, input.endpoint)
  const originChanged =
    nextEndpoint !== input.endpoint && urlOrigin(nextEndpoint) !== urlOrigin(input.endpoint)

  const preset = variantPresets[input.nextVariant]
  const [fallbackEncoding] = variantEncodings[input.nextVariant]
  const nextEncoding =
    fallbackEncoding && !variantEncodings[input.nextVariant].includes(input.encoding)
      ? fallbackEncoding
      : input.encoding

  const nextHeaderRows =
    originChanged ||
    variants.some((candidate) => matchesPresetTextHeaders(candidate, input.headerRows))
      ? textRowsFromRecord(preset.textHeaders)
      : input.headerRows

  const authUnset = input.authKind === 'none' || !input.credentialSecretId
  const resetAuth = originChanged || authUnset

  return {
    endpoint: nextEndpoint,
    encoding: nextEncoding,
    authKind: resetAuth ? preset.authKind : input.authKind,
    authHeaderName: resetAuth ? preset.authHeaderName : input.authHeaderName,
    authUsername: originChanged ? '' : resetAuth ? '' : input.authUsername,
    credentialSecretId: originChanged ? '' : input.credentialSecretId,
    headerRows: nextHeaderRows,
    secretHeaderRows: originChanged ? [] : input.secretHeaderRows,
  }
}
