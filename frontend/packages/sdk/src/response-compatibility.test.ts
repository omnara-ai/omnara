import { describe, expect, it } from 'vitest'

import goldenFixture from '../../../../testdata/bearer-token-v1.json'
import * as previous from '../testdata/pre-canonical-token/zod.gen'
import * as current from './generated/zod.gen'

const id = 'a'.repeat(26)
const timestamp = '2026-08-10T12:00:00Z'

const personalTokenRecord = {
  id: `pat_${id}`,
  user_id: `usr_${id}`,
  name: 'CLI',
  token_id: 'display-id',
  created_at: timestamp,
  last_used_at: null,
  revoked_at: null,
}

const orgKeyRecord = {
  id: `oak_${id}`,
  org_id: `org_${id}`,
  name: 'Automation',
  token_id: 'display-id',
  org_role: 'member',
  created_by_user_id: `usr_${id}`,
  created_at: timestamp,
  updated_at: timestamp,
  last_used_at: null,
  revoked_at: null,
}

const daemonTokenRecord = {
  id: `mdt_${id}`,
  org_id: `org_${id}`,
  machine_id: `mch_${id}`,
  name: 'Daemon',
  metadata: {},
  created_at: timestamp,
  last_used_at: null,
  revoked_at: null,
  revoke_reason: '',
}

function goldenToken(kind: 'pat' | 'org' | 'daemon'): string {
  const token = goldenFixture.vectors.find((vector) => vector.kind === kind)?.token
  if (token === undefined) {
    throw new Error(`missing ${kind} bearer-token golden vector`)
  }
  return token
}

const tokenByKind = {
  pat: goldenToken('pat'),
  org: goldenToken('org'),
  daemon: goldenToken('daemon'),
}

describe('generated response compatibility', () => {
  // These representative schemas become a regression tripwire when a future
  // generator run changes an otherwise unchanged response.
  it.each([
    [
      'current user',
      current.zCurrentUser,
      previous.zCurrentUser,
      {
        user: { id: `usr_${id}`, email: 'user@example.com', display_name: 'User' },
        orgs: [{ id: `org_${id}`, name: 'Org', role: 'owner', created_at: timestamp }],
      },
    ],
    [
      'personal token record',
      current.zPersonalAccessToken,
      previous.zPersonalAccessToken,
      personalTokenRecord,
    ],
    ['organization key record', current.zOrgApiKey, previous.zOrgApiKey, orgKeyRecord],
    ['API error', current.zError, previous.zError, { error: 'not found', code: 'not_found' }],
    [
      'daemon token record',
      current.zMachineDaemonToken,
      previous.zMachineDaemonToken,
      daemonTokenRecord,
    ],
  ] as const)(
    'keeps the %s response readable',
    (_name, currentSchema, previousSchema, response) => {
      expect(currentSchema.safeParse(response).success).toBe(true)
      expect(previousSchema.safeParse(response).success).toBe(true)
    },
  )

  it('records only the approved PAT and organization-key creation breaks', () => {
    const personalResponse = { token: tokenByKind.pat, token_record: personalTokenRecord }
    const orgResponse = { token: tokenByKind.org, api_key: orgKeyRecord }

    expect(current.zCreatePersonalAccessTokenResponse.safeParse(personalResponse).success).toBe(
      true,
    )
    expect(current.zCreateOrgApiKeyResponse.safeParse(orgResponse).success).toBe(true)
    expect(previous.zCreatePersonalAccessTokenResponse.safeParse(personalResponse).success).toBe(
      false,
    )
    expect(previous.zCreateOrgApiKeyResponse.safeParse(orgResponse).success).toBe(false)
  })

  it('keeps new daemon-token creation readable by the previous client', () => {
    const response = { token: tokenByKind.daemon, token_record: daemonTokenRecord }

    expect(current.zCreateMachineDaemonTokenResponse.safeParse(response).success).toBe(true)
    expect(previous.zCreateMachineDaemonTokenResponse.safeParse(response).success).toBe(true)
  })
})
