import type { Secret } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import {
  newSecretDialogState,
  newSecretFormSecret,
  type SecretDialogState,
} from './CreateSecretDialogState'
import { submitSecretTransaction } from './createSecretSubmission'

const createdSecret: Secret = {
  id: 'secret_1',
  org_id: 'org_1',
  management_kind: 'tenant',
  owner: { kind: 'org' },
  name: 'api-token',
  kind: 'generic',
  metadata: {},
  current_version_number: 1,
  payload_keys: ['value'],
  created_at: '2026-08-03T00:00:00Z',
  updated_at: '2026-08-03T00:00:00Z',
}

function genericState(): SecretDialogState {
  return {
    ...newSecretDialogState(),
    name: 'api-token',
    secret: { kind: 'generic', value: 'super-secret' },
    projectGrantIds: ['project_1', 'project_2'],
  }
}

function unusedOAuthStart() {
  return Promise.resolve({
    authorization_url: 'https://auth.example/authorize',
    expires_at: '2026-08-03T01:00:00Z',
  })
}

describe('submitSecretTransaction', () => {
  it('retains a created secret and retries only failed grants', async () => {
    const createSecret = vi.fn(() => Promise.resolve(createdSecret))
    let firstProjectAttempt = true
    const grantSecret = vi.fn(({ projectID }: { secretID: string; projectID: string }) => {
      if (projectID === 'project_1' && firstProjectAttempt) {
        firstProjectAttempt = false
        return Promise.reject(new Error('grant rejected'))
      }
      return Promise.resolve()
    })
    const operations = {
      createSecret,
      grantSecret,
      startMcpOAuth: vi.fn(unusedOAuthStart),
      savePendingMcpGrants: vi.fn(),
    }

    const first = await submitSecretTransaction({
      state: genericState(),
      owner: { kind: 'org' },
      returnTo: '/secrets',
      operations,
    })

    expect(first).toMatchObject({
      kind: 'grant-failures',
      secret: createdSecret,
      failedProjectIds: ['project_1'],
    })
    if (first.kind !== 'grant-failures') throw new Error('expected a grant failure')

    const retry = await submitSecretTransaction({
      state: {
        ...genericState(),
        createdSecret: first.secret,
        projectGrantIds: first.failedProjectIds,
      },
      owner: { kind: 'org' },
      returnTo: '/secrets',
      operations,
    })

    expect(retry).toEqual({ kind: 'complete', secret: createdSecret })
    expect(createSecret).toHaveBeenCalledTimes(1)
    expect(grantSecret.mock.calls.map(([input]) => input.projectID)).toEqual([
      'project_1',
      'project_2',
      'project_1',
    ])
  })

  it('persists pending grants before redirecting to MCP OAuth', async () => {
    const startMcpOAuth = vi.fn(unusedOAuthStart)
    const savePendingMcpGrants = vi.fn()
    const createSecret = vi.fn(() => Promise.resolve(createdSecret))
    const state: SecretDialogState = {
      ...genericState(),
      secret: {
        kind: 'mcp_oauth',
        serverUrl: ' https://mcp.example/server ',
        clientId: ' client-id ',
        clientSecret: ' client-secret ',
      },
    }

    const result = await submitSecretTransaction({
      state,
      owner: { kind: 'project', project_id: 'project_1' },
      returnTo: '/secrets?created=true',
      operations: {
        createSecret,
        grantSecret: vi.fn(() => Promise.resolve()),
        startMcpOAuth,
        savePendingMcpGrants,
      },
    })

    expect(result).toEqual({
      kind: 'redirect',
      authorizationUrl: 'https://auth.example/authorize',
    })
    expect(startMcpOAuth).toHaveBeenCalledWith({
      owner: { kind: 'project', project_id: 'project_1' },
      name: 'api-token',
      mcp_url: 'https://mcp.example/server',
      return_to: '/secrets?created=true',
      client_id: 'client-id',
      client_secret: 'client-secret',
    })
    expect(savePendingMcpGrants).toHaveBeenCalledWith(['project_1', 'project_2'])
    expect(createSecret).not.toHaveBeenCalled()
  })

  it('rejects incomplete OAuth material without creating a secret', async () => {
    const createSecret = vi.fn(() => Promise.resolve(createdSecret))
    const result = await submitSecretTransaction({
      state: { ...genericState(), secret: newSecretFormSecret('oauth_token_set') },
      owner: { kind: 'org' },
      returnTo: '/secrets',
      operations: {
        createSecret,
        grantSecret: vi.fn(() => Promise.resolve()),
        startMcpOAuth: vi.fn(unusedOAuthStart),
        savePendingMcpGrants: vi.fn(),
      },
    })

    expect(result).toEqual({
      kind: 'failed',
      secret: null,
      message: 'OAuth token material is incomplete',
    })
    expect(createSecret).not.toHaveBeenCalled()
  })
})
