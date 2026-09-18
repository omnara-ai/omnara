import { describe, expect, it } from 'vitest'

import { mcpRuntimeToolNameError, mcpServerNameError } from './agentConfigMcp'

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
