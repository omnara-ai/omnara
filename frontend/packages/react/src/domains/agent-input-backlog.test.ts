import { type AgentInput, createOmnaraClient, type ListAgentInputsResponse } from '@omnara/sdk'
import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'

import {
  agentInputBacklogQueryKey,
  cacheAgentInputBacklog,
  isScheduledBacklogInput,
  optimisticBacklogUpdate,
  reorderAgentInputBacklog,
} from './agent-input-backlog'

const scope = { orgID: 'org', projectID: 'project', agentID: 'agent' }

function input(id: string): AgentInput {
  return {
    id,
    agent_id: scope.agentID,
    state: 'received',
    delivery_mode: 'queued',
    input_kind: 'content',
    content_blocks: [{ type: 'text', text: id }],
    queued_at: '2026-08-27T00:00:00Z',
  }
}

function response(data: AgentInput[]): ListAgentInputsResponse {
  return { data, next_cursor: null }
}

describe('optimisticBacklogUpdate', () => {
  it('reapplies a successful mutation after a stale backlog response', async () => {
    const queryClient = new QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const client = createOmnaraClient({ baseUrl: 'http://localhost' })
    const queryKey = agentInputBacklogQueryKey(client, scope)
    const cancelLifecycle = optimisticBacklogUpdate(
      queryClient,
      queryKey,
      (inputs, inputID: string) => inputs.filter((candidate) => candidate.id !== inputID),
    )
    queryClient.setQueryData(queryKey, response([input('input-1'), input('input-2')]))

    await cancelLifecycle.onMutate('input-1')
    queryClient.setQueryData(
      queryKey,
      response([input('input-1'), input('input-2'), input('input-3')]),
    )
    await cancelLifecycle.onSuccess({ ok: true }, 'input-1')
    cancelLifecycle.onSettled()

    expect(queryClient.getQueryData<ListAgentInputsResponse>(queryKey)?.data).toEqual([
      input('input-2'),
      input('input-3'),
    ])
    expect(invalidate).toHaveBeenCalledWith({ queryKey })
  })
})

describe('scheduled backlog inputs', () => {
  const scheduled: AgentInput = {
    ...input('scheduled'),
    state: 'scheduled',
    scheduled_for: '2026-12-01T09:00:00Z',
  }

  it('identifies scheduled inputs', () => {
    expect(isScheduledBacklogInput(scheduled)).toBe(true)
    expect(isScheduledBacklogInput(input('queued'))).toBe(false)
  })

  it('keeps scheduled inputs out of reordering', () => {
    const inputs = [input('queued'), scheduled]
    expect(
      reorderAgentInputBacklog(inputs, {
        inputID: 'scheduled',
        anchorInputID: 'queued',
        position: 'before',
      }),
    ).toBe(inputs)
  })

  it('caches new waiting inputs ahead of scheduled ones', () => {
    const queryClient = new QueryClient()
    const client = createOmnaraClient({ baseUrl: 'http://localhost' })
    const queryKey = agentInputBacklogQueryKey(client, scope)
    queryClient.setQueryData(queryKey, response([input('input-1'), scheduled]))

    cacheAgentInputBacklog(queryClient, client, scope, input('input-2'))

    expect(queryClient.getQueryData<ListAgentInputsResponse>(queryKey)?.data).toEqual([
      input('input-1'),
      input('input-2'),
      scheduled,
    ])
  })
})
