import {
  type AgentInput,
  type ListAgentInputsResponse,
  type OkResponse,
  type OmnaraClient,
  sdk,
} from '@omnara/sdk'
import {
  listQueuedBacklogInputsOptions,
  listQueuedBacklogInputsQueryKey,
} from '@omnara/sdk/tanstack'
import { type QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { isHiddenContentBlock } from './agent-chat-messages'

const backlogQuery = { limit: 100 } as const

interface AgentInputBacklogScope {
  orgID: string
  projectID: string
  agentID: string
}

export type AgentInputBacklogItem =
  | AgentInput
  | { id: string; delivery_mode: 'optimistic'; text: string; attachmentCount: number }

export interface AgentInputBacklogPreview {
  text: string
  attachmentCount: number
}

export function backlogInputPreview(input: AgentInputBacklogItem): AgentInputBacklogPreview {
  if (input.delivery_mode === 'optimistic') {
    return { text: input.text, attachmentCount: input.attachmentCount }
  }
  const lines: string[] = []
  let attachmentCount = 0
  for (const block of input.content_blocks ?? []) {
    if (isHiddenContentBlock(block)) continue
    if (block.type === 'media_ref') attachmentCount += 1
    if (block.type !== 'text') continue
    const text = (block.metadata?.omnara_display_text ?? block.text).trim()
    if (text !== '') lines.push(text)
  }
  if (lines.length > 0) return { text: lines.join('\n'), attachmentCount }
  const text =
    attachmentCount === 0
      ? 'Message'
      : attachmentCount === 1
        ? 'Attachment'
        : `${String(attachmentCount)} attachments`
  return { text, attachmentCount }
}

export interface AgentInputBacklogMove {
  inputID: string
  anchorInputID: string
  position: 'before' | 'after'
}

export function reorderAgentInputBacklog<Input extends AgentInputBacklogItem>(
  inputs: Input[],
  { inputID, anchorInputID, position }: AgentInputBacklogMove,
): Input[] {
  const input = inputs.find((candidate) => candidate.id === inputID)
  const anchor = inputs.find((candidate) => candidate.id === anchorInputID)
  if (
    input == null ||
    anchor == null ||
    input.delivery_mode !== 'queued' ||
    anchor.delivery_mode !== 'queued'
  ) {
    return inputs
  }
  const reordered = inputs.filter((candidate) => candidate.id !== inputID)
  const anchorIndex = reordered.findIndex((candidate) => candidate.id === anchorInputID)
  reordered.splice(position === 'before' ? anchorIndex : anchorIndex + 1, 0, input)
  return reordered
}

export interface AgentInputBacklogControls {
  inputs: AgentInputBacklogItem[]
  actionPending: boolean
  beginCancellation: (inputIDs: string[]) => Promise<() => void>
  cancel: (inputID: string) => Promise<OkResponse>
  promote: (inputID: string) => Promise<OkResponse>
  move: (input: AgentInputBacklogMove) => Promise<OkResponse>
}

export function agentInputBacklogQueryKey(client: OmnaraClient, path: AgentInputBacklogScope) {
  return listQueuedBacklogInputsQueryKey({ path, query: backlogQuery, client })
}

export function cacheAgentInputBacklog(
  queryClient: QueryClient,
  client: OmnaraClient,
  path: AgentInputBacklogScope,
  input: AgentInput,
): void {
  if (input.state !== 'received') return
  const queryKey = agentInputBacklogQueryKey(client, path)
  const current = queryClient.getQueryData<ListAgentInputsResponse>(queryKey)
  if (current == null) return
  const inputs = current.data.filter((candidate) => candidate.id !== input.id)
  const insertionIndex =
    input.delivery_mode === 'steering'
      ? inputs.findIndex((candidate) => candidate.delivery_mode !== 'steering')
      : -1
  inputs.splice(insertionIndex < 0 ? inputs.length : insertionIndex, 0, input)
  queryClient.setQueryData<ListAgentInputsResponse>(queryKey, { ...current, data: inputs })
}

export function optimisticBacklogUpdate<Variables>(
  queryClient: QueryClient,
  queryKey: ReturnType<typeof agentInputBacklogQueryKey>,
  update: (inputs: AgentInput[], variables: Variables) => AgentInput[],
) {
  const applyUpdate = (variables: Variables) => {
    queryClient.setQueryData<ListAgentInputsResponse>(queryKey, (current) =>
      current == null ? current : { ...current, data: update(current.data, variables) },
    )
  }
  return {
    onMutate: async (variables: Variables) => {
      await queryClient.cancelQueries({ queryKey })
      const previous = queryClient.getQueryData<ListAgentInputsResponse>(queryKey)
      applyUpdate(variables)
      return { previous }
    },
    onSuccess: async (_data: OkResponse, variables: Variables) => {
      await queryClient.cancelQueries({ queryKey })
      applyUpdate(variables)
    },
    onError: (
      _error: Error,
      _variables: Variables,
      context: { previous: ListAgentInputsResponse | undefined } | undefined,
    ) => {
      if (context?.previous != null) queryClient.setQueryData(queryKey, context.previous)
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey })
    },
  }
}

export function useAgentInputBacklog(path: AgentInputBacklogScope) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const queryKey = agentInputBacklogQueryKey(client, path)
  const optimisticUpdate = <Variables>(
    update: (inputs: AgentInput[], variables: Variables) => AgentInput[],
  ) => optimisticBacklogUpdate(queryClient, queryKey, update)
  const query = useQuery(listQueuedBacklogInputsOptions({ path, query: backlogQuery, client }))
  const beginCancellation = async (inputIDs: string[], dismissRelatedInputs: () => () => void) => {
    const ids = new Set(inputIDs)
    await queryClient.cancelQueries({ queryKey })
    const previous = queryClient.getQueryData<ListAgentInputsResponse>(queryKey)
    queryClient.setQueryData<ListAgentInputsResponse>(queryKey, (current) =>
      current == null
        ? current
        : { ...current, data: current.data.filter((input) => !ids.has(input.id)) },
    )
    const rollbackRelatedInputs = dismissRelatedInputs()
    return () => {
      if (previous != null) queryClient.setQueryData(queryKey, previous)
      rollbackRelatedInputs()
    }
  }
  const cancel = useMutation({
    mutationFn: async (inputID: string) => {
      const { data } = await sdk.cancelQueuedBacklogInput({
        path: { ...path, inputID },
        client,
      })
      return data
    },
    ...optimisticUpdate((inputs, inputID: string) =>
      inputs.filter((input) => input.id !== inputID),
    ),
  })
  const promote = useMutation({
    mutationFn: async (inputID: string) => {
      const { data } = await sdk.promoteQueuedInputToSteering({
        path: { ...path, inputID },
        client,
      })
      return data
    },
    ...optimisticUpdate((inputs, inputID: string) => {
      const promoted = inputs.find((input) => input.id === inputID)
      if (promoted == null) return inputs
      const remaining = inputs.filter((input) => input.id !== inputID)
      const steering = remaining.filter((input) => input.delivery_mode === 'steering')
      const queued = remaining.filter((input) => input.delivery_mode !== 'steering')
      return [...steering, { ...promoted, delivery_mode: 'steering' as const }, ...queued]
    }),
  })
  const move = useMutation({
    mutationFn: async ({ inputID, anchorInputID, position }: AgentInputBacklogMove) => {
      const { data } = await sdk.moveQueuedBacklogInput({
        path: { ...path, inputID },
        body: { position, anchor_input_id: anchorInputID },
        client,
      })
      return data
    },
    ...optimisticUpdate(reorderAgentInputBacklog),
  })

  return { query, beginCancellation, cancel, promote, move }
}
