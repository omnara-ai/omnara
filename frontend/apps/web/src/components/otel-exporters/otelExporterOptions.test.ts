import { expect, it } from 'vitest'

import { endpointForVariant, headersForVariant, variantEndpoints } from './otelExporterOptions'

function keys(rows: { key: string }[]) {
  return rows.map((row) => row.key)
}

it("fills in a backend's endpoint and headers until the user changes them", () => {
  expect(endpointForVariant('langfuse', '')).toBe(variantEndpoints.langfuse)
  expect(endpointForVariant('braintrust', variantEndpoints.langfuse)).toBe(
    variantEndpoints.braintrust,
  )
  expect(endpointForVariant('otel', variantEndpoints.braintrust)).toBe('')
  expect(endpointForVariant('langsmith', 'https://collector.internal/v1/traces')).toBe(
    'https://collector.internal/v1/traces',
  )

  const langfuse = headersForVariant('langfuse', [], [])
  expect(langfuse.textRows.map((row) => [row.key, row.value])).toEqual([
    ['x-langfuse-ingestion-version', '4'],
  ])
  expect(langfuse.secretRows.map((row) => [row.key, row.secretId])).toEqual([['Authorization', '']])

  // Untouched rows follow the variant; rows the user has filled in are kept.
  const langsmith = headersForVariant('langsmith', langfuse.textRows, langfuse.secretRows)
  expect([keys(langsmith.textRows), keys(langsmith.secretRows)]).toEqual([[], ['x-api-key']])
  const [authorization] = langfuse.secretRows
  const chosen = authorization ? [{ ...authorization, secretId: 'sec_1' }] : []
  const kept = headersForVariant('braintrust', langfuse.textRows, chosen)
  expect([keys(kept.textRows), kept.secretRows]).toEqual([['x-langfuse-ingestion-version'], chosen])
  expect(
    keys(headersForVariant('otel', langsmith.textRows, langsmith.secretRows).secretRows),
  ).toEqual([])
})
