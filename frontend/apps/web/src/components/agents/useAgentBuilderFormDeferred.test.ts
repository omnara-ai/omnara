import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { type BasicConfig, createBasicConfigSession } from './useAgentBuilderForm'

const minimalYaml = `instruction: Do the thing.
model:
  provider_config: anthropic
  name: claude-sonnet-5
`

function mustDeserialize(source: string): BasicConfig {
  const config = createBasicConfigSession(source).initialDraft
  if (config == null) throw new Error('expected the config to deserialize')
  return config
}

function applyToSource(source: string, config: BasicConfig): string {
  return createBasicConfigSession(source).apply(config)
}

describe('createBasicConfigSession deferred tools', () => {
  it('round-trips deferred tools and mcp loading overrides', () => {
    const source = `${minimalYaml}tools:
  web_search: {}
  web_fetch:
    deferred: true
mcp:
  search:
    url: https://mcp.example.com
    deferred: true
    tools:
      lookup:
        deferred: false
`
    const config = mustDeserialize(source)
    expect(config.tools).toEqual([
      { name: 'web_search', permission: null },
      { name: 'web_fetch', permission: null, deferred: true },
    ])
    expect(config.mcpServers).toMatchObject([
      { name: 'search', deferred: true, tools: [{ name: 'lookup', deferred: false }] },
    ])
    expect(applyToSource(source, config)).toBe(source)

    const [server] = config.mcpServers
    if (server == null) throw new Error('missing server')
    const cleared = applyToSource(source, {
      ...config,
      tools: config.tools.map((tool) => ({ ...tool, deferred: undefined })),
      mcpServers: [
        {
          ...server,
          deferred: undefined,
          tools: [{ name: 'lookup', enabled: null, permission: null }],
        },
      ],
    })
    expect(parse(cleared)).toEqual({
      ...parse(minimalYaml),
      tools: { web_search: {}, web_fetch: { type: 'built_in' } },
      mcp: { search: { url: 'https://mcp.example.com', default_enabled: true } },
    })
  })
})
