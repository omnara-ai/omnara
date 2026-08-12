import { describe, expect, it } from 'vitest'

import {
  zCreateMachineDaemonTokenResponse,
  zCreateOrgApiKeyResponse,
  zCreatePersonalAccessTokenResponse,
} from './generated/zod.gen'

describe('generated token response contracts', () => {
  it.each([
    ['personal access token', zCreatePersonalAccessTokenResponse.shape.token],
    ['organization API key', zCreateOrgApiKeyResponse.shape.token],
    ['machine daemon token', zCreateMachineDaemonTokenResponse.shape.token],
  ])('treats the %s as an opaque nonempty string', (_name, schema) => {
    expect(schema.safeParse('future-token-format').success).toBe(true)
    expect(schema.safeParse('').success).toBe(false)
  })
})
