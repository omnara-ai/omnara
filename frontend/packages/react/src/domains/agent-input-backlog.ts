import { type OmnaraClient, sdk } from '@omnara/sdk'
import {
  listQueuedBacklogInputsOptions,
  listQueuedBacklogInputsQueryKey,
} from '@omnara/sdk/tanstack'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

const backlogQuery = { limit: 100 } as const

interface AgentInputBacklogScope {
  orgID: string
  projectID: string
  agentID: string
}

export function agentInputBacklogQueryKey(client: OmnaraClient, path: AgentInputBacklogScope) {
  return listQueuedBacklogInputsQueryKey({ path, query: backlogQuery, client })
}

export function useAgentInputBacklog(path: AgentInputBacklogScope) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const query = useQuery(listQueuedBacklogInputsOptions({ path, query: backlogQuery, client }))
  const cancel = useMutation({
    mutationFn: async (inputID: string) => {
      const { data } = await sdk.cancelQueuedBacklogInput({
        path: { ...path, inputID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: agentInputBacklogQueryKey(client, path),
      })
    },
  })
  const promote = useMutation({
    mutationFn: async (inputID: string) => {
      const { data } = await sdk.promoteQueuedInputToSteering({
        path: { ...path, inputID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: agentInputBacklogQueryKey(client, path),
      })
    },
  })
  const move = useMutation({
    mutationFn: async ({
      inputID,
      anchorInputID,
      position,
    }: {
      inputID: string
      anchorInputID: string
      position: 'before' | 'after'
    }) => {
      const { data } = await sdk.moveQueuedBacklogInput({
        path: { ...path, inputID },
        body: { position, anchor_input_id: anchorInputID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: agentInputBacklogQueryKey(client, path),
      })
    },
  })

  return { query, cancel, promote, move }
}
