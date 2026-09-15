import { once } from 'node:events'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'

import { onTestFinished } from 'vitest'
import { z } from 'zod'

type FixtureJSON = z.infer<ReturnType<typeof z.json>>

export async function slackServer(
  handler: (request: IncomingMessage, response: ServerResponse) => void,
) {
  const server = createServer(handler)
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  onTestFinished(async () => {
    server.closeAllConnections()
    await new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error) reject(error)
        else resolve()
      })
    })
  })
  const address = z.object({ port: z.number() }).parse(server.address())
  return `http://127.0.0.1:${address.port}`
}

export function json(
  response: ServerResponse,
  body: FixtureJSON,
  status = 200,
  headers: Record<string, string> = {},
) {
  response.writeHead(status, { 'content-type': 'application/json', ...headers })
  response.end(JSON.stringify(body))
}

export async function body(request: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = []
  for await (const chunk of request) chunks.push(Buffer.from(z.instanceof(Uint8Array).parse(chunk)))
  return Buffer.concat(chunks)
}

export function operation(durationMs = 10_000) {
  return { requestId: 'slack-operation-1', deadlineMs: Date.now() + durationMs }
}

export function attempt(signal = new AbortController().signal, durationMs = 10_000) {
  return { ...operation(durationMs), attempt: 1, signal }
}

export const credentials = {
  botToken: 'xoxb-local-fixture',
  signingSecret: 'local-signing-fixture',
  botUserId: 'UBOT',
}

export function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
