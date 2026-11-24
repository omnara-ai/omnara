import { expect, it } from 'vitest'

import { secretRowsFromRecord, textRowsFromRecord } from '@/components/key-value/keyValueRows'

import {
  applyVariantSwitch,
  braintrustParentError,
  endpointForVariant,
  variantEndpoints,
} from './otelExporterOptions'

it("fills in a backend's endpoint, auth preset, and headers until customized", () => {
  expect(endpointForVariant('langfuse', '')).toBe(variantEndpoints.langfuse)
  expect(endpointForVariant('braintrust', variantEndpoints.langfuse)).toBe(
    variantEndpoints.braintrust,
  )
  expect(endpointForVariant('otel', variantEndpoints.braintrust)).toBe('')
  expect(endpointForVariant('langsmith', 'https://collector.internal/v1/traces')).toBe(
    'https://collector.internal/v1/traces',
  )

  const langfuse = applyVariantSwitch({
    nextVariant: 'langfuse',
    endpoint: '',
    encoding: 'json',
    authKind: 'none',
    authHeaderName: '',
    authUsername: '',
    credentialSecretId: '',
    headerRows: [],
    secretHeaderRows: [],
  })
  expect(langfuse.endpoint).toBe(variantEndpoints.langfuse)
  expect(langfuse.authKind).toBe('basic')
  expect(langfuse.encoding).toBe('json')
  expect(langfuse.headerRows.map((row) => [row.key, row.value])).toEqual([
    ['x-langfuse-ingestion-version', '4'],
  ])

  // Switching to a different backend origin clears credentials so secrets from
  // Langfuse are never forwarded to Braintrust.
  const braintrust = applyVariantSwitch({
    nextVariant: 'braintrust',
    endpoint: langfuse.endpoint,
    encoding: langfuse.encoding,
    authKind: langfuse.authKind,
    authHeaderName: langfuse.authHeaderName,
    authUsername: 'pk-lf-123',
    credentialSecretId: 'sec_langfuse',
    headerRows: langfuse.headerRows,
    secretHeaderRows: secretRowsFromRecord({ 'x-extra': 'sec_extra' }),
  })
  expect(braintrust.endpoint).toBe(variantEndpoints.braintrust)
  expect(braintrust.encoding).toBe('protobuf')
  expect(braintrust.authKind).toBe('bearer_token')
  expect(braintrust.authUsername).toBe('')
  expect(braintrust.credentialSecretId).toBe('')
  expect(braintrust.secretHeaderRows).toEqual([])
  expect(braintrust.headerRows.map((row) => [row.key, row.value])).toEqual([
    ['x-bt-parent', 'project_id:'],
  ])

  // Switching variant while keeping a custom endpoint preserves credentials on the same origin.
  const custom = applyVariantSwitch({
    nextVariant: 'langsmith',
    endpoint: 'https://collector.internal/v1/traces',
    encoding: 'protobuf',
    authKind: 'bearer_token',
    authHeaderName: '',
    authUsername: '',
    credentialSecretId: 'sec_collector',
    headerRows: textRowsFromRecord({ 'x-team': 'infra' }),
    secretHeaderRows: secretRowsFromRecord({ 'x-extra': 'sec_extra' }),
  })
  expect(custom.endpoint).toBe('https://collector.internal/v1/traces')
  expect(custom.authKind).toBe('bearer_token')
  expect(custom.credentialSecretId).toBe('sec_collector')
  expect(custom.headerRows.map((row) => [row.key, row.value])).toEqual([['x-team', 'infra']])
  expect(custom.secretHeaderRows.map((row) => [row.key, row.secretId])).toEqual([
    ['x-extra', 'sec_extra'],
  ])
})

it('requires a value after the kind in the Braintrust parent header', () => {
  const rows = (value: string) => textRowsFromRecord({ 'x-bt-parent': value })
  expect(braintrustParentError('braintrust', rows('project_id:'))).toBeDefined()
  expect(braintrustParentError('braintrust', rows('project_id: '))).toBeDefined()
  expect(braintrustParentError('braintrust', rows(''))).toBeDefined()
  expect(braintrustParentError('braintrust', rows(':abc'))).toBeDefined()
  // A span.export() parent slug is Base64, with no kind prefix.
  expect(braintrustParentError('braintrust', rows('abc'))).toBeUndefined()
  expect(braintrustParentError('braintrust', rows('AwEBAQIDBAUGBwgJCgsMDQ4P+/8='))).toBeUndefined()
  expect(braintrustParentError('braintrust', rows('project_id:abc'))).toBeUndefined()
  expect(braintrustParentError('braintrust', rows('project_name:my project'))).toBeUndefined()
  expect(braintrustParentError('braintrust', [])).toBeUndefined()
  expect(braintrustParentError('otel', rows('project_id:'))).toBeUndefined()
})
