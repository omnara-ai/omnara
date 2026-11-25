/** @vitest-environment happy-dom */

import { OmnaraClientProvider, type ToolCallUpdateListener } from '@omnara/react'
import { createOmnaraClient, type ToolCallUpdate } from '@omnara/sdk'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

import { clickTool } from './page-actions'
import { usePageActions } from './usePageActions'

const scope = { orgID: fakeId('org'), projectID: fakeId('proj'), agentID: fakeId('agt') }
const toolCallID = fakeId('tcl')
const callPath = `/api/v1/orgs/${scope.orgID}/projects/${scope.projectID}/agents/${scope.agentID}/tool-calls/${toolCallID}`
const ready: ToolCallUpdate = {
  org_id: scope.orgID,
  project_id: scope.projectID,
  agent_id: scope.agentID,
  agent_profile_id: null,
  parent_agent_id: null,
  root_agent_id: scope.agentID,
  tool_call_id: toolCallID,
  state: 'ready',
  name: clickTool,
}

function Actions({
  enabled,
  listen,
}: {
  enabled: boolean
  listen: (l: ToolCallUpdateListener) => void
}) {
  usePageActions(
    scope,
    {
      events: [],
      hasOlderMessages: false,
      subscribeToolCallUpdates: (listener) => {
        listen(listener)
        return () => undefined
      },
    },
    { enabled, actorId: fakeId('act'), tools: undefined },
  )
  return null
}

it.each([
  { whose: 'the viewer’s own assistant', enabled: true, reads: 1 },
  { whose: 'an agent someone else created', enabled: false, reads: 0 },
])('asks Omnara about a ready page call from $whose $reads time(s)', async ({ enabled, reads }) => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  const root = createRoot(container)
  const api = fakeApi([
    {
      method: 'GET',
      path: callPath,
      // Already over, so the call needs nothing further from the page.
      respond: () => jsonResponse({ state: 'completed' }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', maxRetries: 0 })
  client.setConfig({ fetch: api.fetch })
  let announce: ToolCallUpdateListener = () => undefined
  try {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <Actions
            enabled={enabled}
            listen={(listener) => {
              announce = listener
            }}
          />
        </OmnaraClientProvider>,
      )
    })
    await act(async () => {
      announce(ready)
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(api.requestsTo('GET', callPath)).toHaveLength(reads)
  } finally {
    act(() => {
      root.unmount()
    })
    container.remove()
    restore()
  }
})
