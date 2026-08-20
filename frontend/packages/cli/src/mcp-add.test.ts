import { describe, expect, it } from 'vitest'

import { zMcpAddBody } from './mcp-add.ts'

describe('MCP add names', () => {
  it('preserves an explicit valid secret name exactly', () => {
    const result = zMcpAddBody.parse({
      mcp_url: 'https://mcp.example.com',
      server_name: 'docs',
      secret_name: 'R&D MCP 😀',
    })

    expect(result.secret_name).toBe('R&D MCP 😀')
  })

  it.each([' secret', 'secret ', 'x'.repeat(65), 'secret\u200dname'])(
    'rejects invalid secret name %j',
    (secretName) => {
      expect(
        zMcpAddBody.safeParse({
          mcp_url: 'https://mcp.example.com',
          server_name: 'docs',
          secret_name: secretName,
        }).success,
      ).toBe(false)
    },
  )
})
