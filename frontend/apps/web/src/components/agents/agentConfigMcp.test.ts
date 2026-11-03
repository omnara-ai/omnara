import { describe, expect, it } from 'vitest'

import {
  mcpRuntimeToolNameError,
  mcpServerNameError,
  mcpToolEnabled,
  unexposableMcpTools,
} from './agentConfigMcp'
import type { BasicMcpServer } from './useAgentBuilderForm'

function mcpServer(patch: Partial<BasicMcpServer>): BasicMcpServer {
  return {
    id: 'mcp-1',
    name: 'search',
    url: 'https://mcp.example.com',
    permission: { mode: 'always_allow', parameters: {} },
    defaultEnabled: true,
    authType: 'none',
    secretId: '',
    service: '',
    region: '',
    tools: [],
    ...patch,
  }
}

describe('MCP config names', () => {
  it.each([
    ['', 'Name is required.'],
    [' github', 'Name must start with a letter.'],
    ['1github', 'Name must start with a letter.'],
    ['git_hub', 'Name may only contain letters, numbers, and hyphens.'],
    ['a'.repeat(33), 'Name cannot exceed 32 characters.'],
    ['github', undefined],
    ['GitHub-2', undefined],
  ])('reports the MCP server key rule for %j', (name, expected) => {
    expect(mcpServerNameError(name)).toBe(expected)
  })

  it('explains when the prefixed MCP tool name exceeds the model limit', () => {
    const tool = 'provider__search-call-recordings-by-metadata'
    expect(mcpRuntimeToolNameError('cust-read', tool)).toBeUndefined()
    expect(mcpRuntimeToolNameError('customer-user-read', tool)).toBe(
      `"${tool}" becomes "mcp__customer-user-read__${tool}" (69 characters) once the server name is prefixed, ` +
        'but the model only accepts tool names of 64 characters or fewer. ' +
        'Shorten the server name to 13 characters or fewer.',
    )
    expect(mcpRuntimeToolNameError('a', 'b'.repeat(64))).toBe(
      `"${'b'.repeat(64)}" becomes "mcp__a__${'b'.repeat(64)}" (72 characters) once the server name is prefixed, ` +
        'but the model only accepts tool names of 64 characters or fewer. ' +
        'The tool name itself is too long to expose under any server name.',
    )
  })

  it.each([
    ['', 'Tool name is required.'],
    [
      '1search',
      '"1search" must start with a letter, but the model only accepts tool names that begin with a letter.',
    ],
    [
      'search.issues',
      '"search.issues" contains characters other than letters, numbers, underscores, and hyphens, which the model does not accept in tool names.',
    ],
    [
      'search issues',
      '"search issues" contains characters other than letters, numbers, underscores, and hyphens, which the model does not accept in tool names.',
    ],
    ['search_issues-v2', undefined],
  ])('explains when the MCP tool name %j has characters the model rejects', (name, expected) => {
    expect(mcpRuntimeToolNameError('github', name)).toBe(expected)
  })
})

describe('MCP tool exposure', () => {
  it('lists enabled discovered and configured MCP tools the model cannot accept', () => {
    const longName = 'b'.repeat(64)
    const configured = mcpServer({
      name: 'github',
      defaultEnabled: true,
      tools: [
        { name: longName, enabled: null, permission: null },
        { name: 'c'.repeat(64), enabled: false, permission: null },
        { name: 'search.issues', enabled: true, permission: null },
      ],
    })
    expect(
      unexposableMcpTools(configured, ['list-issues', 'get.issue', 'search.issues']).map(
        (tool) => tool.name,
      ),
    ).toEqual(['get.issue', 'search.issues', longName])
    expect(unexposableMcpTools({ ...configured, defaultEnabled: false }, ['get.issue'])).toEqual([
      {
        name: 'search.issues',
        error:
          '"search.issues" contains characters other than letters, numbers, underscores, and hyphens, which the model does not accept in tool names.',
      },
    ])
  })

  it('resolves whether an MCP tool is enabled from its override or the server default', () => {
    const tools = [
      { name: 'on', enabled: true, permission: null },
      { name: 'off', enabled: false, permission: null },
      { name: 'inherit', enabled: null, permission: null },
    ]
    const enabled = mcpServer({ defaultEnabled: true, tools })
    const disabled = mcpServer({ defaultEnabled: false, tools })
    expect(mcpToolEnabled(enabled, 'on')).toBe(true)
    expect(mcpToolEnabled(enabled, 'off')).toBe(false)
    expect(mcpToolEnabled(enabled, 'inherit')).toBe(true)
    expect(mcpToolEnabled(enabled, 'unknown')).toBe(true)
    expect(mcpToolEnabled(disabled, 'on')).toBe(true)
    expect(mcpToolEnabled(disabled, 'off')).toBe(false)
    expect(mcpToolEnabled(disabled, 'inherit')).toBe(false)
    expect(mcpToolEnabled(disabled, 'unknown')).toBe(false)
  })
})
