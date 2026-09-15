import { type Agent, type AgentChange, AgentEventStreamError } from '@omnara/sdk'
import {
  getAgentQueryKey,
  listAgentsInfiniteQueryKey,
  listAgentsQueryKey,
} from '@omnara/sdk/tanstack'
import { QueryClient, type QueryKey, QueryObserver } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  client,
  connection,
  controlEvent,
  delta,
  event,
  resetChatTestHarness,
  scope,
  startSession,
  toolCallBlock,
  toolResultEvent,
} from './agent-chat-test-support'
import { openAgentInteractionsQueryKey } from './agent-interactions'

const cleanup: (() => void)[] = []

function mountQuery<T>(queryClient: QueryClient, queryKey: QueryKey, read: () => T | Promise<T>) {
  const fetch = vi.fn(() => Promise.resolve(read()))
  const observer = new QueryObserver(queryClient, { queryKey, queryFn: fetch })
  cleanup.push(observer.subscribe(() => undefined))
  return { fetch, observer }
}

function mountDeferredQuery<T>(queryClient: QueryClient, queryKey: QueryKey, before: T, fresh: T) {
  let release!: () => void
  const pending = new Promise<T>((resolve) => {
    release = () => {
      resolve(before)
    }
  })
  let reads = 0
  const query = mountQuery(queryClient, queryKey, () => (++reads === 1 ? pending : fresh))
  return { ...query, release, fresh }
}

async function setup({ changeBeforeConnect = false } = {}) {
  const apiClient = client()
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } },
  })
  cleanup.push(() => {
    queryClient.clear()
  })
  const path = { orgID: scope.orgID, projectID: scope.projectID }
  let rows: { id: string; activity: string; archived: boolean }[] = []
  let interactions: string[] = []
  let parent: Pick<Agent, 'id' | 'state'> = { id: scope.agentID, state: 'active' }
  const childList = mountQuery(
    queryClient,
    listAgentsInfiniteQueryKey({
      client: apiClient,
      path,
      query: {
        parent_agent_id: scope.agentID,
        include_archived: true,
        sort: 'created_at',
        limit: 50,
      },
    }),
    () => rows,
  )
  const grandchildList = mountQuery(
    queryClient,
    listAgentsInfiniteQueryKey({ client: apiClient, path, query: { parent_agent_id: 'child' } }),
    () => [],
  )
  const unrelatedChildList = mountQuery(
    queryClient,
    listAgentsInfiniteQueryKey({ client: apiClient, path, query: { parent_agent_id: 'other' } }),
    () => [],
  )
  const otherProjectList = mountQuery(
    queryClient,
    listAgentsInfiniteQueryKey({
      client: apiClient,
      path: { ...path, projectID: 'other-project' },
      query: { parent_agent_id: scope.agentID },
    }),
    () => [],
  )
  const projectList = mountQuery(
    queryClient,
    listAgentsQueryKey({ client: apiClient, path }),
    () => [],
  )
  const openInteractions = mountQuery(
    queryClient,
    openAgentInteractionsQueryKey(apiClient, scope),
    () => interactions,
  )
  const otherInteractions = mountQuery(
    queryClient,
    openAgentInteractionsQueryKey(apiClient, { ...scope, agentID: 'other' }),
    () => [],
  )
  const parentDetail = mountQuery(
    queryClient,
    getAgentQueryKey({ client: apiClient, path: scope }),
    () => parent,
  )
  const otherDetail = mountQuery(
    queryClient,
    getAgentQueryKey({ client: apiClient, path: { ...scope, agentID: 'other' } }),
    () => ({ id: 'other', state: 'active' }),
  )
  const childDetail = mountQuery(
    queryClient,
    getAgentQueryKey({ client: apiClient, path: { ...scope, agentID: 'child' } }),
    () => rows[0] ?? null,
  )
  const queries = [
    childList,
    grandchildList,
    unrelatedChildList,
    otherProjectList,
    projectList,
    openInteractions,
    otherInteractions,
    parentDetail,
    otherDetail,
    childDetail,
  ]
  await vi.waitFor(() => {
    for (const query of queries) expect(query.observer.getCurrentResult().isSuccess).toBe(true)
  })
  if (changeBeforeConnect) {
    parent = { id: scope.agentID, state: 'archived' }
    rows = [{ id: 'new-child', activity: 'running', archived: false }]
    interactions = ['missed question']
  }
  const session = startSession([], apiClient, queryClient)
  cleanup.push(session.disconnect)
  const stream = await connection(0)
  await vi.waitFor(() => {
    for (const query of [parentDetail, childList, openInteractions]) {
      expect(query.fetch).toHaveBeenCalledTimes(2)
    }
    for (const query of queries) expect(query.observer.getCurrentResult().isFetching).toBe(false)
  })
  // Count only refreshes caused by the actions after the initial connection.
  for (const query of queries) query.fetch.mockClear()
  let sequence = 0
  // A durable text frame is a consumption barrier: every earlier frame has been handled.
  async function flush() {
    sequence += 1
    stream.push({ event: 'model_output', data: event({ sequence, content_blocks: [] }) })
    await vi.waitFor(() => {
      expect(session.getData().events.at(-1)?.sequence).toBe(sequence)
    })
  }
  async function change(data: AgentChange) {
    stream.push({ event: 'agent_change', data })
    await flush()
  }
  return {
    childList,
    grandchildList,
    unrelatedChildList,
    otherProjectList,
    projectList,
    openInteractions,
    otherInteractions,
    parentDetail,
    otherDetail,
    childDetail,
    session,
    stream,
    flush,
    change,
    setParent: (value: typeof parent) => {
      parent = value
    },
    setRows: (value: typeof rows) => {
      rows = value
    },
    setInteractions: (value: string[]) => {
      interactions = value
    },
  }
}

describe('agent change cache refresh', () => {
  beforeEach(resetChatTestHarness)
  afterEach(() => {
    for (const dispose of cleanup.splice(0).reverse()) dispose()
  })

  it('recovers changes committed between initial reads and the first stream connection', async () => {
    const h = await setup({ changeBeforeConnect: true })
    expect(h.parentDetail.observer.getCurrentResult().data?.state).toBe('archived')
    expect(h.childList.observer.getCurrentResult().data?.[0]?.id).toBe('new-child')
    expect(h.openInteractions.observer.getCurrentResult().data).toEqual(['missed question'])
  })

  it.each(['connection', 'agent_change'] as const)(
    'replaces pending initial reads after %s and ignores their late responses',
    async (trigger) => {
      const apiClient = client()
      const queryClient = new QueryClient({
        defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } },
      })
      cleanup.push(() => {
        queryClient.clear()
      })
      if (trigger === 'agent_change') {
        const session = startSession([], apiClient, queryClient)
        cleanup.push(session.disconnect)
        await connection(0)
        // Let initial connection recovery finish before mounting the delayed queries.
        await new Promise((resolve) => setTimeout(resolve, 0))
      }
      const detailAgentID = trigger === 'connection' ? scope.agentID : 'child'
      const queries = [
        mountDeferredQuery(
          queryClient,
          getAgentQueryKey({ client: apiClient, path: { ...scope, agentID: detailAgentID } }),
          { id: detailAgentID, state: 'active' },
          { id: detailAgentID, state: 'archived' },
        ),
        mountDeferredQuery(
          queryClient,
          listAgentsInfiniteQueryKey({
            client: apiClient,
            path: { orgID: scope.orgID, projectID: scope.projectID },
            query: { parent_agent_id: scope.agentID },
          }),
          [],
          [{ id: 'child' }],
        ),
        mountDeferredQuery(
          queryClient,
          openAgentInteractionsQueryKey(apiClient, scope),
          [],
          ['question'],
        ),
      ]
      for (const query of queries) {
        expect(query.fetch).toHaveBeenCalledTimes(1)
        expect(query.observer.getCurrentResult().data).toBeUndefined()
      }
      if (trigger === 'connection') {
        const session = startSession([], apiClient, queryClient)
        cleanup.push(session.disconnect)
        await connection(0)
      } else {
        const stream = await connection(0)
        stream.push({
          event: 'agent_change',
          data: {
            agent_id: 'child',
            parent_agent_id: scope.agentID,
            changes: ['agent', 'interactions'],
          },
        })
      }
      await vi.waitFor(() => {
        for (const query of queries) {
          expect(query.fetch).toHaveBeenCalledTimes(2)
          expect(query.observer.getCurrentResult().data).toEqual(query.fresh)
        }
      })
      // These old requests ignore cancellation, so late results must also be harmless.
      for (const query of queries) query.release()
      await new Promise((resolve) => setTimeout(resolve, 0))
      for (const query of queries)
        expect(query.observer.getCurrentResult().data).toEqual(query.fresh)
    },
  )

  it('refetches the mounted child list on creation, activity, and archive', async () => {
    const h = await setup()
    for (const [index, row] of [
      { id: 'child', activity: 'idle', archived: false },
      { id: 'child', activity: 'running', archived: false },
      { id: 'child', activity: 'idle', archived: true },
    ].entries()) {
      h.setRows([row])
      await h.change({ agent_id: 'child', parent_agent_id: scope.agentID, changes: ['agent'] })
      expect(h.childList.fetch).toHaveBeenCalledTimes(index + 1)
      expect(h.childList.observer.getCurrentResult().data).toEqual([row])
      expect(h.childDetail.observer.getCurrentResult().data).toEqual(row)
    }
    for (const query of [
      h.openInteractions,
      h.parentDetail,
      h.grandchildList,
      h.unrelatedChildList,
      h.otherProjectList,
      h.projectList,
    ]) {
      expect(query.fetch).toHaveBeenCalledTimes(0)
    }
    expect(h.session.getData().deltas).toEqual([])
    expect(h.session.getData().error).toBeUndefined()
  })

  it('refreshes interactions while the parent is idle and updates only the source detail and parent-filtered list', async () => {
    const h = await setup()
    h.setInteractions(['question'])
    await h.change({ agent_id: 'child', parent_agent_id: scope.agentID, changes: ['interactions'] })
    expect(h.openInteractions.observer.getCurrentResult().data).toEqual(['question'])
    expect(h.childList.fetch).toHaveBeenCalledTimes(1)
    expect(h.childDetail.fetch).toHaveBeenCalledTimes(1)
    expect(h.parentDetail.fetch).toHaveBeenCalledTimes(0)
    expect(h.otherInteractions.fetch).toHaveBeenCalledTimes(0)
    expect(h.projectList.fetch).toHaveBeenCalledTimes(0)

    h.setInteractions([])
    await h.change({ agent_id: scope.agentID, parent_agent_id: null, changes: ['interactions'] })
    expect(h.openInteractions.observer.getCurrentResult().data).toEqual([])
    expect(h.parentDetail.fetch).toHaveBeenCalledTimes(1)
    expect(h.childList.fetch).toHaveBeenCalledTimes(1)
  })

  it('uses the actual changed parent for grandchild interactions', async () => {
    const h = await setup()
    h.setInteractions(['grandchild question'])
    await h.change({ agent_id: 'grandchild', parent_agent_id: 'child', changes: ['interactions'] })
    expect(h.openInteractions.observer.getCurrentResult().data).toEqual(['grandchild question'])
    expect(h.grandchildList.fetch).toHaveBeenCalledTimes(1)
    for (const query of [
      h.childList,
      h.unrelatedChildList,
      h.projectList,
      h.otherProjectList,
      h.parentDetail,
      h.childDetail,
      h.otherInteractions,
    ]) {
      expect(query.fetch).toHaveBeenCalledTimes(0)
    }
  })

  it('does not refetch lists or interactions for ordinary tool updates, durable tools, or deltas', async () => {
    const h = await setup()
    h.stream.push({
      event: 'tool_call_update',
      data: { tool_call_id: 'call', agent_id: 'child', state: 'ready' },
    })
    h.stream.push({
      event: 'model_output',
      data: event({ sequence: 1, content_blocks: [toolCallBlock()] }),
    })
    h.stream.push({ event: 'tool_result', data: toolResultEvent({ sequence: 2 }) })
    h.stream.push({
      event: 'model_output_delta',
      data: delta(
        1,
        { kind: 'text_delta', block_index: 0, delta: 'hello' },
        { model_call_context_id: 'next' },
      ),
    })
    await vi.waitFor(() => {
      expect(h.session.getData().deltas).toHaveLength(1)
    })
    h.stream.push({
      event: 'model_output',
      data: event({ sequence: 3, content_blocks: [{ type: 'text', text: 'done' }] }),
    })
    await vi.waitFor(() => {
      expect(h.session.getData().events.at(-1)?.sequence).toBe(3)
    })
    for (const query of [
      h.childList,
      h.grandchildList,
      h.projectList,
      h.openInteractions,
      h.otherInteractions,
      h.parentDetail,
      h.childDetail,
    ]) {
      expect(query.fetch).toHaveBeenCalledTimes(0)
    }
    expect(h.session.getData().error).toBeUndefined()

    h.stream.push({ event: 'agent_input', data: controlEvent({ sequence: 4 }) })
    await vi.waitFor(() => {
      expect(h.openInteractions.fetch).toHaveBeenCalledTimes(1)
    })
    expect(h.childList.fetch).toHaveBeenCalledTimes(0)
  })

  it('recovers agent detail, child lists, and scoped interactions on reconnect', async () => {
    const h = await setup()
    expect(h.parentDetail.observer.getCurrentResult().data?.state).toBe('active')
    h.stream.connectionState({
      state: 'reconnecting',
      attempt: 1,
      delayMs: 100,
      error: new AgentEventStreamError({ kind: 'transport', message: 'disconnected' }),
    })
    h.setParent({ id: scope.agentID, state: 'archived' })
    h.setRows([{ id: 'new-child', activity: 'running', archived: false }])
    h.setInteractions(['missed question'])
    h.stream.connectionState({ state: 'connected', reconnected: true })
    await vi.waitFor(() => {
      expect(h.parentDetail.observer.getCurrentResult().data?.state).toBe('archived')
      expect(h.childList.observer.getCurrentResult().data?.[0]?.id).toBe('new-child')
      expect(h.openInteractions.observer.getCurrentResult().data).toEqual(['missed question'])
    })
    for (const query of [
      h.grandchildList,
      h.unrelatedChildList,
      h.projectList,
      h.otherProjectList,
      h.otherInteractions,
      h.otherDetail,
      h.childDetail,
    ]) {
      expect(query.fetch).toHaveBeenCalledTimes(0)
    }
  })
})
