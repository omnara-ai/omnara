import type { Secret } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { prepareSecretReplacement, requiredSecretKeys } from './secretValueUpdates'

function secretValueUpdatesError(current: Secret, values: Record<string, string>) {
  return prepareSecretReplacement(current, values).error
}

function secret(kind: Secret['kind'], payload_keys: string[]): Secret {
  return {
    id: 'sec_1',
    org_id: 'org_1',
    name: 'test',
    kind,
    payload_keys,
    management_kind: 'tenant',
    owner: { kind: 'org' },
    metadata: {},
    current_version_number: 1,
    created_at: '',
    updated_at: '',
  }
}

describe('secret value field edits', () => {
  it('requires a complete replacement and explicit removal of stored optional fields', () => {
    const current = secret('aws_credentials', [
      'access_key_id',
      'secret_access_key',
      'session_token',
    ])
    expect(secretValueUpdatesError(current, { secret_access_key: 'new-key' })).toBeDefined()
    expect(
      secretValueUpdatesError(current, {
        access_key_id: 'new-id',
        secret_access_key: 'new-key',
        session_token: '',
      }),
    ).toBeUndefined()
    expect(
      secretValueUpdatesError(current, {
        access_key_id: '',
        secret_access_key: 'new-key',
        session_token: '',
      }),
    ).toBeDefined()
  })
  it('rejects incomplete OAuth refresh credentials', () => {
    const current = secret('oauth_token_set', ['access_token'])
    expect(
      secretValueUpdatesError(current, { access_token: 'new', refresh_token: 'refresh' }),
    ).toBeDefined()
  })
  it('distinguishes unchanged from explicitly cleared generic values', () => {
    const current = secret('generic', ['value'])
    expect(secretValueUpdatesError(current, {})).toBeUndefined()
    expect(secretValueUpdatesError(current, { value: '' })).toBeDefined()
  })
})

it('validates AWS dependencies and normalized required fields', () => {
  const current = secret('aws_credentials', ['access_key_id', 'secret_access_key'])
  const values = { access_key_id: ' id ', secret_access_key: ' key ' }
  expect(secretValueUpdatesError(current, values)).toBeUndefined()
  expect(prepareSecretReplacement(current, values).material).toEqual({
    kind: 'aws_credentials',
    access_key_id: 'id',
    secret_access_key: 'key',
  })
  expect(secretValueUpdatesError(current, { ...values, access_key_id: ' ' })).toBeDefined()
  expect(secretValueUpdatesError(current, { ...values, external_id: 'external' })).toBeDefined()
  expect(
    secretValueUpdatesError(current, { ...values, external_id: 'external', role_arn: 'role' }),
  ).toBeUndefined()
})
it('accepts a complete OAuth refresh set', () => {
  expect(
    secretValueUpdatesError(secret('oauth_token_set', ['access_token']), {
      access_token: 'access',
      refresh_token: 'refresh',
      token_endpoint: 'https://example.com/token',
      client_id: 'client',
      resource: 'resource',
    }),
  ).toBeUndefined()
})

it.each(['0', '1.5', '2147483648'])(
  'rejects invalid OAuth lifetime %s before building material',
  (lifetime) => {
    const result = prepareSecretReplacement(
      secret('oauth_token_set', ['access_token']),
      { access_token: 'access' },
      lifetime,
    )
    expect(result.error).toBeDefined()
    expect(result.material).toBeUndefined()
  },
)

it('rejects unsupported credentials before validating their fields', () => {
  const current = secret('slack_app_credentials', [
    'access_token',
    'client_id',
    'client_secret',
    'signing_secret',
  ])
  expect(requiredSecretKeys(current.kind)).toBeUndefined()
  expect(prepareSecretReplacement(current, {})).toEqual({})
  expect(prepareSecretReplacement(current, { access_token: 'replacement' })).toEqual({
    error: 'These credentials cannot be replaced here.',
  })
})
