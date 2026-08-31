import { describe, expect, it } from 'vitest'

import { findServerByRemoteUrl, normalizeRemoteUrl } from './mcp-registry'

const base = {
  description: '',
  version: '1',
  status: 'active',
  updated_at: '2026-08-20T00:00:00Z',
  icons: [],
}

describe('normalizeRemoteUrl', () => {
  it('drops the scheme and trailing slashes and lowercases', () => {
    expect(normalizeRemoteUrl(' HTTPS://MCP.Linear.app/mcp/ ')).toBe('mcp.linear.app/mcp')
    expect(normalizeRemoteUrl('linear.app')).toBe('linear.app')
    expect(normalizeRemoteUrl('')).toBe('')
  })
})

describe('findServerByRemoteUrl', () => {
  it('returns only the server with an exactly matching remote', () => {
    const linear = {
      ...base,
      name: 'app.linear/linear',
      remotes: [{ type: 'streamable-http', url: 'https://mcp.linear.app/mcp' }],
    }
    const other = {
      ...base,
      name: 'app.linear/linear-v2',
      remotes: [{ type: 'streamable-http', url: 'https://mcp.linear.app/mcp-v2' }],
    }
    expect(findServerByRemoteUrl([other, linear], 'http://mcp.linear.app/mcp/')).toBe(linear)
    expect(findServerByRemoteUrl([other], 'https://mcp.linear.app/mcp')).toBeNull()
    expect(findServerByRemoteUrl([linear], '')).toBeNull()
  })
})
