import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'

export const eventWebhookEventTypes = [
  { value: 'agent_input', label: 'Agent input' },
  { value: 'model_output', label: 'Model output' },
  { value: 'tool_result', label: 'Tool result' },
  { value: 'context_checkpoint', label: 'Context checkpoint' },
  { value: 'tool_call_update', label: 'Tool-call updates' },
]

export function eventWebhookWire(
  config: Pick<
    BasicConfig,
    'eventWebhookUrl' | 'eventWebhookSigningSecretId' | 'eventWebhookEvents'
  >,
) {
  const url = config.eventWebhookUrl.trim()
  if (!url) return null
  const signingSecretId = config.eventWebhookSigningSecretId.trim()
  const webhook = signingSecretId ? { url, signing_secret_id: signingSecretId } : { url }
  return config.eventWebhookEvents === null
    ? webhook
    : { ...webhook, events: config.eventWebhookEvents }
}

export function eventWebhookUrlError(value: string): string | undefined {
  if (value.trim() === '') return undefined
  try {
    const url = new URL(value.trim())
    if (url.protocol !== 'https:') return 'Use an HTTPS URL.'
  } catch {
    return 'Enter a valid HTTPS URL.'
  }
  return undefined
}
