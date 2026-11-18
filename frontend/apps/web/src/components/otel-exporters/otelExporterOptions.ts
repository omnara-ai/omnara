import type { OtelExporterEncoding, OtelExporterScope, OtelTraceVariant } from '@omnara/sdk'

import {
  type SecretRow,
  secretRowsFromRecord,
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

/**
 * The endpoint to show after switching variant: the new backend's own, unless the user has
 * typed one that is not just another backend's default.
 */
export function endpointForVariant(variant: OtelTraceVariant, current: string) {
  const untouched = current.trim() === '' || Object.values(variantEndpoints).includes(current)
  return untouched ? variantEndpoints[variant] : current
}

/**
 * The headers each backend needs. Secret headers hold credentials, so they start with no secret
 * chosen; text headers start with the value shown, which Braintrust's needs completing.
 */
const variantHeaders: Record<OtelTraceVariant, { text: Record<string, string>; secret: string[] }> =
  {
    otel: { text: {}, secret: [] },
    // Authorization is "Basic " plus base64 of "public key:secret key".
    langfuse: { text: { 'x-langfuse-ingestion-version': '4' }, secret: ['Authorization'] },
    // Authorization is "Bearer " plus an API key; x-bt-parent names the project to log to.
    braintrust: { text: { 'x-bt-parent': 'project_id:' }, secret: ['Authorization'] },
    langsmith: { text: {}, secret: ['x-api-key'] },
  }

function matchesPreset(variant: OtelTraceVariant, textRows: TextRow[], secretRows: SecretRow[]) {
  const preset = variantHeaders[variant]
  const text = Object.entries(preset.text)
  return (
    textRows.length === text.length &&
    text.every(([key, value]) => textRows.some((row) => row.key === key && row.value === value)) &&
    secretRows.length === preset.secret.length &&
    preset.secret.every((key) => secretRows.some((row) => row.key === key && !row.secretId))
  )
}

/**
 * The header rows to show after switching variant: the new backend's own, unless the user has
 * already changed the rows from another backend's starting set.
 */
export function headersForVariant(
  variant: OtelTraceVariant,
  textRows: TextRow[],
  secretRows: SecretRow[],
) {
  if (!variants.some((candidate) => matchesPreset(candidate, textRows, secretRows)))
    return { textRows, secretRows }
  const preset = variantHeaders[variant]
  return {
    textRows: textRowsFromRecord(preset.text),
    secretRows: secretRowsFromRecord(Object.fromEntries(preset.secret.map((key) => [key, '']))),
  }
}

/** Whether every resource the scope names has been chosen. */
export function otelExporterScopeComplete(scope: OtelExporterScope) {
  if (scope.type === 'org') return true
  return !!scope.project_id && (scope.type === 'project' || !!scope.agent_id)
}
