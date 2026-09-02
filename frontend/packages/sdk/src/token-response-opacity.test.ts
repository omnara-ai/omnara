import { describe, expect, it } from 'vitest'

import {
  zConnectByoMachineResponse,
  zCreateMachineDaemonTokenResponse,
  zCreateOrgApiKeyResponse,
  zCreatePersonalAccessTokenResponse,
} from './generated/zod.gen'

describe('generated token response contracts', () => {
  it.each([
    ['personal access token', zCreatePersonalAccessTokenResponse.pick({ token: true })],
    ['organization API key', zCreateOrgApiKeyResponse.pick({ token: true })],
    ['machine daemon token', zCreateMachineDaemonTokenResponse.pick({ token: true })],
    ['connected machine daemon token', zConnectByoMachineResponse.pick({ token: true })],
  ])('treats the %s as an opaque nonempty string', (_name, schema) => {
    expect(schema.safeParse({ token: 'future-token-format' }).success).toBe(true)
    expect(schema.safeParse({ token: '' }).success).toBe(false)
  })
})
