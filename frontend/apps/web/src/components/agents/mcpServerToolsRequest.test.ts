import { describe, expect, it } from 'vitest'

import { mcpServerToolsRequest } from '@/components/agents/mcpServerToolsRequest'

const base = { url: ' https://mcp.example.com/mcp ', secretId: '', service: '', region: '' }

describe('mcpServerToolsRequest', () => {
  it('connects without auth as soon as the URL is usable', () => {
    expect(mcpServerToolsRequest({ ...base, authType: 'none' })).toEqual({
      url: 'https://mcp.example.com/mcp',
      auth: { type: 'none' },
    })
    expect(mcpServerToolsRequest({ ...base, url: 'mcp.example.com', authType: 'none' })).toBeNull()
    expect(mcpServerToolsRequest({ ...base, url: '', authType: 'none' })).toBeNull()
  })

  it('waits for a secret before connecting with bearer or oauth auth', () => {
    expect(mcpServerToolsRequest({ ...base, authType: 'bearer' })).toBeNull()
    expect(mcpServerToolsRequest({ ...base, authType: 'oauth', secretId: 'sec_1' })).toEqual({
      url: 'https://mcp.example.com/mcp',
      auth: { type: 'oauth', secret_id: 'sec_1' },
    })
  })

  it('requires the signing service and region for sigv4', () => {
    expect(mcpServerToolsRequest({ ...base, authType: 'sigv4', secretId: 'sec_1' })).toBeNull()
    expect(
      mcpServerToolsRequest({
        ...base,
        authType: 'sigv4',
        secretId: 'sec_1',
        service: 'execute-api',
        region: 'us-east-1',
      }),
    ).toEqual({
      url: 'https://mcp.example.com/mcp',
      auth: { type: 'sigv4', secret_id: 'sec_1', service: 'execute-api', region: 'us-east-1' },
    })
  })
})
