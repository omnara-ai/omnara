import { fileURLToPath } from 'node:url'

import { serve } from '@hono/node-server'
import { ApiError, bearerToken, createOmnaraClient, sdk, type OmnaraClient } from '@omnara/sdk'
import { zEventWebhookPayload } from '@omnara/sdk/zod'
import { Hono } from 'hono'
import { bodyLimit } from 'hono/body-limit'
import { Webhook } from 'standardwebhooks'

export function createWebhookApp(options: {
  client: OmnaraClient
  orgID: string
  projectID: string
  signingSecret: string
}) {
  const verifier = new Webhook(options.signingSecret)

  async function execute(agentID: string, toolCallID: string) {
    const path = {
      orgID: options.orgID,
      projectID: options.projectID,
      agentID,
    }
    let cursor: string | undefined
    do {
      const { data: page } = await sdk.listToolCalls({
        client: options.client,
        path,
        query: { type: 'custom', state: 'ready', cursor },
      })
      const call = page.data.find((item) => item.id === toolCallID)
      if (call) {
        if (call.name !== 'text_length') throw new Error(`unsupported tool: ${call.name}`)
        const text = call.input.text
        const body =
          typeof text === 'string'
            ? {
                outcome: 'succeeded' as const,
                content_blocks: [
                  {
                    type: 'structured_data' as const,
                    value: { length: Array.from(text).length },
                  },
                ],
              }
            : {
                outcome: 'failed' as const,
                content_blocks: [{ type: 'text' as const, text: 'text must be a string' }],
              }
        try {
          await sdk.submitToolCallResult({
            client: options.client,
            path: { ...path, toolCallID },
            body,
          })
        } catch (error) {
          if (!(error instanceof ApiError && error.status === 409)) throw error
        }
        return
      }
      cursor = page.next_cursor ?? undefined
    } while (cursor)
  }

  const app = new Hono()
  app.post('/webhook', bodyLimit({ maxSize: 64 * 1024 }), async (c) => {
    let payload
    try {
      const verified = verifier.verify(await c.req.text(), c.req.header())
      payload = zEventWebhookPayload.parse(verified)
    } catch {
      return c.text('invalid webhook', 400)
    }
    if (payload.event === 'tool_call_update' && payload.data.state === 'ready') {
      const { agent_id: agentID, tool_call_id: toolCallID } = payload.data
      if (!agentID) return c.text('missing agent_id', 400)
      await execute(agentID, toolCallID)
    }
    return c.body(null, 204)
  })
  return app
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  function required(name: string) {
    const value = process.env[name]
    if (!value) throw new Error(`set ${name} in .env`)
    return value
  }
  const app = createWebhookApp({
    client: createOmnaraClient({
      baseUrl: process.env.OMNARA_API_URL ?? 'https://api.omnara.com/v1',
      auth: bearerToken(required('OMNARA_API_KEY')),
    }),
    orgID: required('OMNARA_ORG_ID'),
    projectID: required('OMNARA_PROJECT_ID'),
    signingSecret: required('WEBHOOK_SIGNING_SECRET'),
  })
  serve(
    { fetch: app.fetch, port: Number(process.env.PORT ?? 3000), hostname: '127.0.0.1' },
    (info) => {
      console.log(`webhook receiver listening on http://127.0.0.1:${info.port}/webhook`)
    },
  )
}
