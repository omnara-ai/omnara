import { type AgentInput, ApiError, type OmnaraClient, sdk } from '@omnara/sdk'

const maxReconnectDelayMs = 30_000

export function reconnectBackoff(baseDelayMs: number, consecutiveFailures: number): number {
  const exponent = Math.min(Math.max(consecutiveFailures - 1, 0), 30)
  return Math.min(baseDelayMs * 2 ** exponent, maxReconnectDelayMs)
}

export function isDefiniteSendFailure(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return error.status >= 400 && error.status < 500 && ![408, 429].includes(error.status)
}

export function abortableDelay(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve()
  return new Promise((resolve) => {
    const timeout = globalThis.setTimeout(done, ms)
    function done() {
      globalThis.clearTimeout(timeout)
      signal.removeEventListener('abort', done)
      resolve()
    }
    signal.addEventListener('abort', done, { once: true })
  })
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
