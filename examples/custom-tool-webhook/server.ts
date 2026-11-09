import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { serve } from '@hono/node-server'
import {
  ApiError,
  bearerToken,
  createCustomToolWorker,
  createOmnaraClient,
  CustomToolDeliveryError,
  type CustomToolHandler,
  type OmnaraClient,
  verifyEventWebhook,
} from '@omnara/sdk'
import { Hono } from 'hono'
import { bodyLimit } from 'hono/body-limit'

const textLength: CustomToolHandler = async ({ input }) =>
  typeof input.text === 'string'
    ? {
        outcome: 'succeeded',
        content_blocks: [
          {
            type: 'structured_data',
            value: { length: Array.from(input.text).length },
          },
        ],
      }
    : {
        outcome: 'failed',
        content_blocks: [{ type: 'text', text: 'text must be a string' }],
      }

export function createWebhookApp(options: {
  client: OmnaraClient
  orgID: string
  projectID: string
  agentProfileID?: string
  signingSecret: string
  signal?: AbortSignal
}) {
  const worker = createCustomToolWorker({
    client: options.client,
    path: { orgID: options.orgID, projectID: options.projectID },
    agentProfileID: options.agentProfileID,
    tools: { text_length: textLength },
  })
  const active = new Set<Promise<void>>()
  let cursor: string | undefined

  function report(error: unknown) {
    if (error instanceof CustomToolDeliveryError) {
      if (error.cause instanceof ApiError && [400, 413, 422].includes(error.cause.status)) {
        console.error(
          `Result rejected for ${error.call.toolCallID}; inspect the payload and submit a corrected or explicit failed result. Repeating this calculation will not repair a permanent rejection.`,
          error.cause,
        )
        return
      }
      console.error(
        `Result delivery failed for ${error.call.toolCallID}; the next recovery check will safely recompute text_length.`,
        error.cause,
      )
    } else {
      console.error('Custom tool processing failed; the next recovery check will retry:', error)
    }
  }

  // This calculation has no external effects and is safe to repeat, even after restart.
  // For business actions, persist/reuse CustomToolDeliveryError.result instead and
  // make the action idempotent by tool-call ID across processes.
  async function check() {
    const page = await worker.check({
      cursor,
      limit: 10,
      signal: options.signal,
    })
    for (const { outcome } of page.results) {
      if (outcome.status === 'failed') report(outcome.error)
    }
    cursor = page.nextCursor ?? undefined
  }

  const app = new Hono()
  app.post('/webhook', bodyLimit({ maxSize: 128 * 1024 }), async (c) => {
    let payload
    try {
      payload = await verifyEventWebhook({
        body: await c.req.text(),
        headers: c.req.raw.headers,
        signingSecret: options.signingSecret,
      })
    } catch {
      return c.text('invalid webhook', 400)
    }
    if (payload.event === 'tool_call_update') {
      const done = worker
        .handle(payload.data, { signal: options.signal })
        .then(() => undefined)
        .catch(report)
      active.add(done)
      void done.finally(() => active.delete(done))
    }
    return c.body(null, 204)
  })
  return { app, check, drain: () => Promise.all([...active]) }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  function required(name: string) {
    const value = process.env[name]
    if (!value) throw new Error(`set ${name} in .env`)
    return value
  }
  const stop = new AbortController()
  const { app, check, drain } = createWebhookApp({
    client: createOmnaraClient({
      baseUrl: process.env.OMNARA_API_URL ?? 'https://api.omnara.com/v1',
      auth: bearerToken(required('OMNARA_API_KEY')),
      timeoutMs: 30_000,
    }),
    orgID: required('OMNARA_ORG_ID'),
    projectID: required('OMNARA_PROJECT_ID'),
    agentProfileID: process.env.OMNARA_AGENT_PROFILE_ID || undefined,
    signingSecret: required('WEBHOOK_SIGNING_SECRET'),
    signal: stop.signal,
  })
  const server = serve(
    {
      fetch: app.fetch,
      port: Number(process.env.PORT ?? 3000),
      hostname: '127.0.0.1',
    },
    (info) => {
      console.log(`webhook receiver listening on http://127.0.0.1:${info.port}/webhook`)
    },
  )
  const close = () => {
    server.close()
    stop.abort()
  }
  process.once('SIGTERM', close)
  process.once('SIGINT', close)
  try {
    // Scheduling belongs to this application. Each SDK check reads one bounded page;
    // the next scheduled invocation resumes its cursor, including while webhooks flow.
    while (!stop.signal.aborted) {
      try {
        await check()
      } catch (error) {
        if (!stop.signal.aborted) console.error('Recovery check failed:', error)
      }
      await delay(60_000, undefined, { signal: stop.signal })
    }
  } catch (error) {
    if (!stop.signal.aborted) throw error
  } finally {
    server.close()
    await drain()
    process.removeListener('SIGTERM', close)
    process.removeListener('SIGINT', close)
  }
}
