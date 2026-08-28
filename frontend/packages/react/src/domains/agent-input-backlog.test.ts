import type { AgentInput, ListAgentInputsResponse, OmnaraClient } from '@omnara/sdk'
import * as TanStackReactQuery from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const hookMocks = vi.hoisted(() => ({
  useMutation: vi.fn(),
  useOmnaraClient: vi.fn(),
  useQuery: vi.fn(),
  useQueryClient: vi.fn(),
}))

vi.mock('@tanstack/react-query', async (importOriginal) => ({
  ...(await importOriginal<typeof TanStackReactQuery>()),
  useMutation: hookMocks.useMutation,
  useQuery: hookMocks.useQuery,
  useQueryClient: hookMocks.useQueryClient,
}))

vi.mock('../omnara-client', () => ({
  useOmnaraClient: hookMocks.useOmnaraClient,
}))

import { agentInputBacklogQueryKey, useAgentInputBacklog } from './agent-input-backlog'

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

describe('useAgentInputBacklog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('reapplies a successful mutation after a stale backlog response', async () => {
    const queryClient = new TanStackReactQuery.QueryClient()
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
    const client = {
      getConfig: () => ({ baseUrl: 'http://localhost' }),
    } as unknown as OmnaraClient
    const mutationOptions: unknown[] = []
    hookMocks.useOmnaraClient.mockReturnValue(client)
    hookMocks.useQueryClient.mockReturnValue(queryClient)
    hookMocks.useQuery.mockReturnValue({})
    hookMocks.useMutation.mockImplementation((options: unknown) => {
      mutationOptions.push(options)
      return {}
    })
    useAgentInputBacklog(scope)

    const queryKey = agentInputBacklogQueryKey(client, scope)
    const response = (data: AgentInput[]): ListAgentInputsResponse => ({ data, next_cursor: null })
    queryClient.setQueryData(queryKey, response([input('input-1'), input('input-2')]))
    const cancelMutation = mutationOptions[0] as {
      onMutate: (inputID: string) => Promise<unknown>
      onSuccess: (data: unknown, inputID: string) => Promise<void>
      onSettled: () => void
    }

    await cancelMutation.onMutate('input-1')
    queryClient.setQueryData(
      queryKey,
      response([input('input-1'), input('input-2'), input('input-3')]),
    )
    await cancelMutation.onSuccess(undefined, 'input-1')
    cancelMutation.onSettled()

    expect(queryClient.getQueryData<ListAgentInputsResponse>(queryKey)?.data).toEqual([
      input('input-2'),
      input('input-3'),
    ])
    expect(invalidate).toHaveBeenCalledWith({ queryKey })
  })
})
