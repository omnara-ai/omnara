import { type AgentInput, ApiError, type OmnaraClient, sdk } from '@omnara/sdk'

import type { AgentChatMessageInput, LocalAgentInput } from './agent-chat-types'

export function isDefiniteSendFailure(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return error.status >= 400 && error.status < 500 && ![408, 429].includes(error.status)
}

export function sameMessage(
  previous: LocalAgentInput,
  message: Required<AgentChatMessageInput>,
): boolean {
  const previousAttachments = previous.attachments ?? []
  if (previous.text !== message.text || previousAttachments.length !== message.attachments.length) {
    return false
  }
  return previousAttachments.every((attachment, index) => {
    const candidate = message.attachments[index]
    return (
      attachment.data === candidate?.data &&
      attachment.mediaType === candidate.mediaType &&
      attachment.filename === candidate.filename
    )
  })
}

export async function createAgentChatInput(
  client: OmnaraClient,
  path: { orgID: string; projectID: string; agentID: string },
  id: string,
  message: Required<AgentChatMessageInput>,
  signal?: AbortSignal,
): Promise<AgentInput> {
  const { data } = await sdk.createAgentInput({
    client,
    path,
    headers: { 'Idempotency-Key': id },
    body: {
      content_blocks: [
        {
          type: 'text',
          text: 'This message came from the Omnara web app. Reply with normal assistant text unless explicitly asked to message an integration.',
          metadata: { omnara_hidden: 'true' },
        },
        ...(message.text === '' ? [] : [{ type: 'text' as const, text: message.text }]),
        ...message.attachments.map((attachment) => ({
          type: 'media' as const,
          media_type: attachment.mediaType,
          filename: attachment.filename,
          data: attachment.data,
        })),
      ],
    },
    ...(signal == null ? {} : { signal }),
  })
  return data.agent_input
}
