import { mkdtemp, readdir, rm } from 'node:fs/promises'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, expect, vi } from 'vitest'

import {
  type OperationExecutionResult,
  OperationsHandler,
  type OperationsOptions,
  operationsRoute,
  operationWorkBytes,
} from './operations'
import { GatewayServer } from './server'
import type { GatewayLogger } from './types'
import { WorkByteBudget } from './work-budget'

export const credential = 'omnara_connector_v1_private-test-credential'
export const capability = { connector_key: 'test_connector', provider: 'slack' }
export const auth = { authorization: `Bearer ${credential}` }
const cleanup: (() => Promise<void>)[] = []
afterEach(async () => {
  for (const close of cleanup.splice(0).reverse()) await close()
})

export function envelope() {
  return {
    request_id: 'request-1',
    capability,
    kind: 'send',
    scope: {
      project_id: 'project',
      integration_app_id: 'app',
      integration_install_id: 'install',
      agent_id: 'agent',
      channel_id: 'channel',
    },
    deadline: new Date(Date.now() + 5_000).toISOString(),
    payload: { message: { text: 'hello' } },
  }
}

export function part(
  name: string,
  body: string | Buffer,
  filename?: string,
  contentType = 'application/json',
): Buffer<ArrayBuffer> {
  return Buffer.concat([
    Buffer.from(
      `--boundary\r\nContent-Disposition: form-data; name="${name}"${filename === undefined ? '' : `; filename="${filename}"`}\r\nContent-Type: ${contentType}\r\n\r\n`,
    ),
    Buffer.isBuffer(body) ? body : Buffer.from(body),
    Buffer.from('\r\n'),
  ])
}

export function multipart(body: string | Buffer = 'abc', filename = 'a.txt'): Buffer<ArrayBuffer> {
  const metadata = {
    ...envelope(),
    artifacts: [{ id: 'artifact-1', filename, content_type: 'text/plain' }],
  }
  return Buffer.concat([
    part('operation', JSON.stringify(metadata)),
    part('artifact', body, filename, 'text/plain'),
    Buffer.from('--boundary--\r\n'),
  ])
}

export async function start(
  execute: OperationsOptions['execute'],
  overrides: Partial<OperationsOptions> = {},
) {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), 'omnara-operations-test-'))
  const workBudget = new WorkByteBudget(2 * operationWorkBytes)
  const logger = {
    debug: vi.fn<GatewayLogger['debug']>(),
    info: vi.fn<GatewayLogger['info']>(),
    warn: vi.fn<GatewayLogger['warn']>(),
    error: vi.fn<GatewayLogger['error']>(),
  }
  const operations: Omit<OperationsOptions, 'workBudget'> = {
    allowedCapabilities: [capability],
    credential,
    execute,
    temporaryDirectory,
    maxConcurrentRequests: 2,
    maxTemporaryBytes: 200 * 1024 * 1024,
    maxRequestBytes: 256 * 1024 * 1024,
    maxDurationMs: 5_000,
    ...overrides,
  }
  const server = new GatewayServer({
    bodyLimitBytes: 1024,
    handlerTimeoutMs: 1_000,
    httpShutdownTimeoutMs: 100,
    logger,
    maxConcurrentRequests: 8,
    port: 0,
    publicUrl: 'http://gateway.invalid',
    registry: {
      acquire: () => Promise.reject(new Error('unexpected runtime acquisition')),
      webhookTimeoutMs: () => undefined,
      webhookBodyLimitBytes: () => undefined,
    },
    operations,
    workBudget,
  })
  cleanup.push(async () => {
    await server.close()
    await rm(temporaryDirectory, { recursive: true, force: true })
  })
  const port = await server.listen()
  return {
    port,
    server,
    logger,
    temporaryDirectory,
    workBudget,
    url: `http://127.0.0.1:${port}${operationsRoute}`,
  }
}

export const completed = (): Promise<OperationExecutionResult> =>
  Promise.resolve({ outcome: 'completed', payload: { publication: 'draft' } })

// Exercise the same Node request/parser path without consuming a TCP port.
export async function receiveMultipart(execute: OperationsOptions['execute'], body: Buffer) {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), 'omnara-operations-parser-test-'))
  const handler = new OperationsHandler({
    credential,
    allowedCapabilities: [capability],
    execute,
    temporaryDirectory,
    maxConcurrentRequests: 1,
    maxTemporaryBytes: 1024 * 1024,
    maxRequestBytes: 1024 * 1024,
    maxDurationMs: 1000,
    workBudget: new WorkByteBudget(operationWorkBytes),
  })
  const incoming = new IncomingMessage(new Socket())
  incoming.headers = { ...auth, 'content-type': 'multipart/form-data; boundary=boundary' }
  incoming.rawHeaders = ['Authorization', `Bearer ${credential}`]
  incoming.complete = true
  incoming.push(body)
  incoming.push(null)
  const outgoing = new ServerResponse(incoming)
  try {
    return await handler.handle(incoming, outgoing)
  } finally {
    await handler.close()
    incoming.destroy()
    outgoing.destroy()
    expect(await readdir(temporaryDirectory)).toEqual([])
    await rm(temporaryDirectory, { recursive: true, force: true })
  }
}
