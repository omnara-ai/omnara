import type { EventWebhookPayload } from './generated/types.gen'
import { zEventWebhookPayload } from './generated/zod.gen'
import { relaxedSchema } from './validate-response'

const webhookPayload = relaxedSchema(zEventWebhookPayload)

/** Verify the original, unmodified request body before using its contents. */
export async function verifyEventWebhook(options: {
  body: string
  headers: HeadersInit
  signingSecret: string
}): Promise<EventWebhookPayload> {
  const { Webhook } = await import('standardwebhooks')
  const verified = new Webhook(options.signingSecret).verify(
    options.body,
    Object.fromEntries(new Headers(options.headers)),
  )
  return webhookPayload.parse(verified)
}
