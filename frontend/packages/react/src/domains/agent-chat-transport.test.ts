import type { GetAgentResponse } from '@omnara/sdk'
import { getAgentQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient } from '@tanstack/react-query'
import { expect, it } from 'vitest'

import { client, scope } from './agent-chat-test-support'
import { resolveAgentChatHint, sourceHint } from './agent-chat-transport'

it('keeps the integration reply guidance alongside the captured dashboard context', async () => {
  const api = client()
  const cache = new QueryClient()
  const detail: GetAgentResponse = {
    agent: {
      id: scope.agentID,
      org_id: scope.orgID,
      project_id: scope.projectID,
      name: 'Connected agent',
      state: 'active',
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      integration_target: { provider: 'slack', conversation: {}, display_name: '#support' },
    },
    machine_ids: [],
    mcp_connections: [],
  }
  cache.setQueryData(getAgentQueryKey({ client: api, path: scope }), detail)
  expect(await resolveAgentChatHint(api, cache, scope, 'web', 'Captured page')).toBe(
    `${sourceHint('web')}\n\nCaptured page`,
  )
  delete detail.agent.integration_target
  cache.setQueryData(getAgentQueryKey({ client: api, path: scope }), { ...detail })
  expect(await resolveAgentChatHint(api, cache, scope, 'web', 'Captured page')).toBe(
    'Captured page',
  )
  expect(await resolveAgentChatHint(api, cache, scope, 'web')).toBeUndefined()
  cache.clear()
})
