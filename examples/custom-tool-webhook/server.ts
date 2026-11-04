import { fileURLToPath } from 'node:url'

import { serve } from '@hono/node-server'
import {
  bearerToken,
  createCustomToolWorker,
  createOmnaraClient,
  type CustomToolHandler,
  type OmnaraClient,
} from '@omnara/sdk'
import { zEventWebhookPayload } from '@omnara/sdk/zod'
import { Hono } from 'hono'
import { bodyLimit } from 'hono/body-limit'
import { Webhook } from 'standardwebhooks'

const textLength: CustomToolHandler = async ({ input }) =>
  typeof input.text === 'string'
    ? {
        outcome: 'succeeded',
        content_blocks: [
          { type: 'structured_data', value: { length: Array.from(input.text).length } },
        ],
      }
    : { outcome: 'failed', content_blocks: [{ type: 'text', text: 'text must be a string' }] }

export function createWebhookApp(options: {
  client: OmnaraClient
  orgID: string
  projectID: string
  agentProfileID?: string
  signingSecret: string
  signal?: AbortSignal
}) {
  const verifier = new Webhook(options.signingSecret)
  const worker = createCustomToolWorker({
    client: options.client,
    path: { orgID: options.orgID, projectID: options.projectID },
    agentProfileID: options.agentProfileID,
    tools: { text_length: textLength },
    signal: options.signal,
    onError: (error, call) => {
      console.error(`${call.name} ${call.toolCallID} failed:`, error)
    },
  })

  const app = new Hono()
  app.post('/webhook', bodyLimit({ maxSize: 128 * 1024 }), async (c) => {
    let payload
    try {
      const verified = verifier.verify(await c.req.text(), c.req.header())
      payload = zEventWebhookPayload.parse(verified)
    } catch {
      return c.text('invalid webhook', 400)
    }
    if (payload.event === 'tool_call_update') worker.handle(payload.data)
    return c.body(null, 204)
  })
  return { app, worker }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  function required(name: string) {
    const value = process.env[name]
    if (!value) throw new Error(`set ${name} in .env`)
    return value
  }
  const { app, worker } = createWebhookApp({
    client: createOmnaraClient({
      baseUrl: process.env.OMNARA_API_URL ?? 'https://api.omnara.com/v1',
      auth: bearerToken(required('OMNARA_API_KEY')),
    }),
    orgID: required('OMNARA_ORG_ID'),
    projectID: required('OMNARA_PROJECT_ID'),
    agentProfileID: process.env.OMNARA_AGENT_PROFILE_ID || undefined,
    signingSecret: required('WEBHOOK_SIGNING_SECRET'),
  })
  serve(
    { fetch: app.fetch, port: Number(process.env.PORT ?? 3000), hostname: '127.0.0.1' },
    (info) => {
      console.log(`webhook receiver listening on http://127.0.0.1:${info.port}/webhook`)
    },
  )
  await worker.run()
}
