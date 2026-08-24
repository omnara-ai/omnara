import { describe, expect, it } from 'vitest'

import { zMcpAddBody } from './mcp-add.ts'

describe('MCP add names', () => {
  it('uses the resource-name contract for explicit secret names', () => {
    const result = zMcpAddBody.parse({
      mcp_url: 'https://mcp.example.com',
      server_name: 'docs',
      secret_name: 'Cafe\u0301 MCP',
    })
    expect(result.secret_name).toBe('Café MCP')
    expect(
      zMcpAddBody.safeParse({
        mcp_url: 'https://mcp.example.com',
        server_name: 'docs',
        secret_name: ' invalid',
      }).success,
    ).toBe(false)
  })
})
