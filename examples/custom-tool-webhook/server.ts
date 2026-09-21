import { createServer } from 'node:http'
import { fileURLToPath } from 'node:url'

import { ApiError, bearerToken, createOmnaraClient, sdk, type OmnaraClient } from '@omnara/sdk'
import { zEventWebhookPayload } from '@omnara/sdk/zod'
import { Webhook } from 'standardwebhooks'

export function createWebhookServer(options: {
  client: OmnaraClient
  orgID: string
  projectID: string
  signingSecret: string
}) {
  const verifier = new Webhook(options.signingSecret)
  const inFlight = new Map<string, Promise<void>>()

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

  return createServer(async (request, response) => {
    if (request.method !== 'POST' || request.url !== '/webhook') {
      response.writeHead(404).end()
      return
    }
    try {
      const chunks: Buffer[] = []
      let size = 0
      for await (const chunk of request) {
        size += chunk.length
        if (size > 64 * 1024) {
          response.writeHead(413).end()
          return
        }
        chunks.push(chunk)
      }
      let payload
      try {
        const verified = verifier.verify(Buffer.concat(chunks), {
          'webhook-id': String(request.headers['webhook-id'] ?? ''),
          'webhook-timestamp': String(request.headers['webhook-timestamp'] ?? ''),
          'webhook-signature': String(request.headers['webhook-signature'] ?? ''),
        })
        payload = zEventWebhookPayload.parse(verified)
      } catch {
        response.writeHead(400).end('invalid webhook')
        return
      }
      if (payload.event === 'tool_call_update' && payload.data.state === 'ready') {
        const { agent_id: agentID, tool_call_id: toolCallID } = payload.data
        if (!agentID) {
          response.writeHead(400).end('missing agent_id')
          return
        }
        let work = inFlight.get(toolCallID)
        if (!work) {
          work = execute(agentID, toolCallID)
          inFlight.set(toolCallID, work)
        }
        try {
          await work
        } finally {
          inFlight.delete(toolCallID)
        }
      }
      response.writeHead(204).end()
    } catch (error) {
      console.error('webhook handling failed', error)
      response.writeHead(500).end()
    }
  })
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  function required(name: string) {
    const value = process.env[name]
    if (!value) throw new Error(`set ${name} in .env`)
    return value
  }
  const server = createWebhookServer({
    client: createOmnaraClient({
      baseUrl: process.env.OMNARA_API_URL ?? 'https://api.omnara.com/v1',
      auth: bearerToken(required('OMNARA_API_KEY')),
    }),
    orgID: required('OMNARA_ORG_ID'),
    projectID: required('OMNARA_PROJECT_ID'),
    signingSecret: required('WEBHOOK_SIGNING_SECRET'),
  })
  server.listen(Number(process.env.PORT ?? 3000), '127.0.0.1', () => {
    console.log('webhook receiver listening', server.address())
  })
}
