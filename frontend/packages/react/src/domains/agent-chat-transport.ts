import { type AgentInput, ApiError, type OmnaraClient, sdk } from '@omnara/sdk'

export function isDefiniteSendFailure(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return error.status >= 400 && error.status < 500 && ![408, 429].includes(error.status)
}

export async function createAgentChatInput(
  client: OmnaraClient,
  path: { orgID: string; projectID: string; agentID: string },
  id: string,
  text: string,
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
        { type: 'text', text },
      ],
    },
    ...(signal == null ? {} : { signal }),
  })
  return data.agent_input
}
