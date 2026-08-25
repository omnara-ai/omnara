/** @vitest-environment happy-dom */

import { beforeEach, describe, expect, it } from 'vitest'

import {
  hasPendingMcpBuilderOAuthOutcome,
  savePendingMcpBuilderOAuth,
  takeMcpBuilderOAuthRestore,
} from './pendingMcpBuilderOAuth'
import { type BasicConfig, emptyBasicConfig } from './useAgentBuilderForm'

const draft: BasicConfig = {
  ...emptyBasicConfig,
  instruction: 'Research things',
  mcpServers: [
    {
      id: 'server_1',
      name: 'github',
      url: 'https://mcp.example.com',
      permission: null,
      defaultEnabled: true,
      authType: 'oauth',
      secretId: '',
      service: '',
      region: '',
    },
  ],
}

function visit(path: string, search: string) {
  window.history.replaceState(null, '', `${path}${search}`)
}

describe('takeMcpBuilderOAuthRestore', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
  })

  it('restores the draft with the connected secret selected', () => {
    savePendingMcpBuilderOAuth({
      returnPath: '/projects/p1/agents/new',
      serverId: 'server_1',
      agentName: 'Builder agent',
      draft,
    })
    visit('/projects/p1/agents/new', '?mcp_oauth=success&secret_id=sec_a')

    expect(hasPendingMcpBuilderOAuthOutcome()).toBe(true)
    const restored = takeMcpBuilderOAuthRestore()
    expect(restored).toEqual({
      agentName: 'Builder agent',
      draft: { ...draft, mcpServers: [{ ...draft.mcpServers[0], secretId: 'sec_a' }] },
    })
    expect(window.sessionStorage.length).toBe(0)
    expect(takeMcpBuilderOAuthRestore()).toEqual(restored)
  })

  it('restores the draft without a secret when the flow failed', () => {
    savePendingMcpBuilderOAuth({
      returnPath: '/projects/p1/agents/new',
      serverId: 'server_1',
      agentName: null,
      draft,
    })
    visit('/projects/p1/agents/new', '?mcp_oauth_error=access_denied&r=1')

    expect(takeMcpBuilderOAuthRestore()).toEqual({ agentName: null, draft })
  })

  it('ignores records saved for a different page', () => {
    savePendingMcpBuilderOAuth({
      returnPath: '/projects/p1/agents/new',
      serverId: 'server_1',
      agentName: null,
      draft,
    })
    visit('/projects/p2/agents/new', '?mcp_oauth=success&secret_id=sec_b')

    expect(takeMcpBuilderOAuthRestore()).toBeNull()
    expect(window.sessionStorage.length).toBe(0)
  })

  it('returns nothing without an OAuth outcome in the URL', () => {
    savePendingMcpBuilderOAuth({
      returnPath: '/projects/p1/agents/new',
      serverId: 'server_1',
      agentName: null,
      draft,
    })
    visit('/projects/p1/agents/new', '?r=2')

    expect(hasPendingMcpBuilderOAuthOutcome()).toBe(false)
    expect(takeMcpBuilderOAuthRestore()).toBeNull()
    expect(window.sessionStorage.length).toBe(1)
  })
})
