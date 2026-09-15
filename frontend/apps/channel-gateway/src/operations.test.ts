import { mkdtemp, readdir, rm } from 'node:fs/promises'
import { IncomingMessage, request as httpRequest, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { OperationRetryError } from './operation-retry'
import {
  type OperationArtifact,
  type OperationExecutionResult,
  OperationsHandler,
  type OperationsOptions,
  operationsRoute,
  operationWorkBytes,
} from './operations'
import { GatewayServer } from './server'
import { deferred, incompleteRequest, streamedRequest } from './server-test-support'
import type { GatewayLogger } from './types'
import { WorkByteBudget } from './work-budget'

const credential = 'omnara_connector_v1_private-test-credential'
const capability = { connector_key: 'chat_sdk_v1', provider: 'slack' }
const auth = { authorization: `Bearer ${credential}` }
const cleanup: (() => Promise<void>)[] = []
afterEach(async () => {
  for (const close of cleanup.splice(0).reverse()) await close()
})

function envelope() {
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

function part(
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

function multipart(body: string | Buffer = 'abc', filename = 'a.txt'): Buffer<ArrayBuffer> {
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

async function start(
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
    registry: { acquire: () => Promise.reject(new Error('unexpected runtime acquisition')) },
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

const completed = (): Promise<OperationExecutionResult> =>
  Promise.resolve({ outcome: 'completed', payload: { publication: 'draft' } })

// Exercise the same Node request/parser path without consuming a TCP port.
async function receiveMultipart(execute: OperationsOptions['execute'], body: Buffer) {
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

describe('private channel operations HTTP receiver', () => {
  it('interoperates with the real Go client for all kinds and a streamed Unicode-named artifact', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(async (operation, artifacts) => {
      expect(operation.requestId).toBe(`interop-${operation.kind}`)
      expect(operation.capability).toEqual(capability)
      expect(operation.scope).toEqual(envelope().scope)
      expect(operation.payloadJSON).toBe('{"integer":9007199254740993}')
      if (operation.kind === 'send') {
        expect(artifacts).toHaveLength(1)
        const artifact = artifacts[0]
        if (!artifact) throw new Error('missing artifact')
        expect(artifact.filename).toBe('résumé.txt')
        expect(artifact.sizeBytes).toBe(12 * 1024 * 1024)
        let bytes = 0
        for await (const chunk of artifact.open()) {
          if (!Buffer.isBuffer(chunk)) throw new Error('expected bytes')
          expect(chunk.every((byte) => byte === 120)).toBe(true)
          bytes += chunk.byteLength
        }
        expect(bytes).toBe(artifact.sizeBytes)
      } else {
        expect(artifacts).toHaveLength(0)
      }
      return completed()
    })
    const { url } = await start(execute, {
      credential: 'omnara_connector_v1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_3Q2mUc',
    })
    const result = await promisify(execFile)(
      'go',
      ['run', './internal/channelconnector/testdata/gateway-interop', url],
      {
        cwd: fileURLToPath(new URL('../../../../', import.meta.url)),
        timeout: 25_000,
        maxBuffer: 64 * 1024,
      },
    )
    expect(result.stdout).toBe('interop completed\n')
    expect(execute.mock.calls.map(([operation]) => operation.kind)).toEqual([
      'send',
      'read',
      'interaction',
    ])
  }, 30_000)

  it('accepts authenticated send/read/interaction envelopes and keeps payload integers exact', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url, workBudget } = await start(execute)
    for (const kind of ['send', 'read', 'interaction']) {
      const body = JSON.stringify({ ...envelope(), kind }).replace('"hello"', '9007199254740993')
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body,
      })
      expect(response.status).toBe(200)
      expect(await response.json()).toEqual({
        request_id: 'request-1',
        outcome: 'completed',
        payload: { publication: 'draft' },
      })
    }
    expect(execute).toHaveBeenCalledTimes(3)
    expect(execute.mock.calls[0]?.[0].payloadJSON).toContain('9007199254740993')
    expect(execute.mock.calls[0]?.[1]).toEqual([])
    expect(execute.mock.calls[0]?.[2].aborted).toBe(true)
    expect(workBudget.usedBytes).toBe(0)
  })

  it('rejects missing/wrong/malformed credentials before consuming uploads', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port, url, logger } = await start(execute)
    for (const authorization of [
      '',
      'Bearer wrong',
      `Basic ${credential}`,
      `Bearer ${credential},other`,
    ]) {
      const response = await fetch(url, {
        method: 'POST',
        headers: { authorization },
        body: credential,
      })
      expect(response.status).toBe(401)
      expect(await response.text()).not.toContain(credential)
    }
    expect(await incompleteRequest(port, operationsRoute, { 'content-length': '500' })).toEqual({
      connection: 'close',
      status: 401,
    })
    expect(execute).not.toHaveBeenCalled()
    expect(logger.error).not.toHaveBeenCalled()
  })

  it('rejects duplicate Authorization headers and method/path alternatives', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port, url } = await start(execute)
    const duplicate = await new Promise<number>((resolve, reject) => {
      const request = httpRequest(
        {
          host: '127.0.0.1',
          port,
          path: operationsRoute,
          method: 'POST',
          headers: [
            'Host',
            '127.0.0.1',
            'Authorization',
            `Bearer ${credential}`,
            'Authorization',
            `Bearer ${credential}`,
          ],
        },
        (response) => {
          response.resume()
          resolve(response.statusCode ?? 0)
        },
      )
      request.on('error', reject)
      request.end()
    })
    expect(duplicate).toBe(401)
    expect((await fetch(url, { headers: auth })).status).toBe(405)
    expect((await fetch(`${url}/extra`, { method: 'POST', headers: auth })).status).toBe(404)
    expect(execute).not.toHaveBeenCalled()
  })

  it('rejects invalid scopes, capability pairs, kinds, duplicate keys, and trailing JSON', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    const bodies = [
      JSON.stringify({ ...envelope(), scope: { ...envelope().scope, channel_id: '' } }),
      JSON.stringify({ ...envelope(), capability: { ...capability, provider: 'discord' } }),
      JSON.stringify({ ...envelope(), capability: { ...capability, connector_key: 'other' } }),
      JSON.stringify({ ...envelope(), kind: 'delete' }),
      JSON.stringify({ ...envelope(), version: 'compatibility' }),
      JSON.stringify({
        ...envelope(),
        artifacts: [{ id: 'a', filename: 'x', content_type: 'text/plain' }],
      }),
      JSON.stringify(envelope()).replace('"hello"', '{"x":1,"\\u0078":2}'),
      `${JSON.stringify(envelope())} {}`,
      JSON.stringify({ ...envelope(), deadline: '2026-02-30T00:00:00Z' }),
    ]
    for (const body of bodies) {
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body,
      })
      expect(response.status, body).toBe(400)
    }
    expect(execute).not.toHaveBeenCalled()
  })

  it('fully writes temporary artifacts before execute and removes them on completion', async () => {
    let retainedArtifact: OperationArtifact | undefined
    const execute = vi.fn<OperationsOptions['execute']>(async (_operation, artifacts, signal) => {
      expect(signal.aborted).toBe(false)
      const artifact = artifacts[0]
      if (!artifact) throw new Error('missing artifact')
      retainedArtifact = artifact
      expect(artifact.sizeBytes).toBe(3)
      let text = ''
      for await (const chunk of artifact.open()) text += String(chunk)
      expect(text).toBe('abc')
      return { outcome: 'completed', payload: { publication: 'published' } }
    })
    const { url, temporaryDirectory, workBudget } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'multipart/form-data; boundary=boundary' },
      body: multipart(),
    })
    expect(response.status).toBe(200)
    expect(await response.json()).toMatchObject({ outcome: 'completed' })
    expect(execute).toHaveBeenCalledOnce()
    expect(await readdir(temporaryDirectory)).toEqual([])
    expect(() => retainedArtifact?.open()).toThrow()
    expect(workBudget.usedBytes).toBe(0)
  })

  it('requires the terminal boundary and every declared artifact before execution', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url, temporaryDirectory } = await start(execute)
    for (const body of [
      multipart().subarray(0, -16),
      Buffer.concat([
        part(
          'operation',
          JSON.stringify({
            ...envelope(),
            artifacts: [{ id: 'a', filename: 'a.txt', content_type: 'text/plain' }],
          }),
        ),
        Buffer.from('--boundary--\r\n'),
      ]),
      Buffer.concat([
        part('artifact', 'abc', 'a.txt', 'text/plain'),
        part('operation', JSON.stringify(envelope())),
        Buffer.from('--boundary--\r\n'),
      ]),
      multipart().toString().replace('name="artifact"', 'name="unexpected"'),
      multipart().toString().replace('filename="a.txt"', 'filename="../a.txt"'),
      multipart().toString().replace('name="artifact"', 'name="artifact"; name="another"'),
      multipart()
        .toString()
        .replace(
          'Content-Type: text/plain',
          'Content-Type: text/plain\r\nContent-Type: application/json',
        ),
    ]) {
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'multipart/form-data; boundary=boundary' },
        body,
      })
      expect(response.status).toBe(400)
      expect(await readdir(temporaryDirectory)).toEqual([])
    }
    expect(execute).not.toHaveBeenCalled()
  })

  it('streams stored artifacts larger than the incoming 10 MiB upload ceiling', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url, temporaryDirectory } = await start(execute)
    for (const size of [10 * 1024 * 1024, 10 * 1024 * 1024 + 1, 12 * 1024 * 1024]) {
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'multipart/form-data; boundary=boundary' },
        body: multipart(Buffer.alloc(size, 1)),
      })
      expect(response.status).toBe(200)
      expect(await readdir(temporaryDirectory)).toEqual([])
    }
    expect(execute).toHaveBeenCalledTimes(3)
  })

  it('meters actual multipart request bytes against the deployment budget', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const body = multipart()
    const { port, temporaryDirectory } = await start(execute, { maxRequestBytes: body.length })
    for (const content of [body, Buffer.concat([body, Buffer.from('x')])]) {
      // A stream has no declared Content-Length; the receiver must count bytes.
      const response = await streamedRequest(port, operationsRoute, [content.toString()], {
        ...auth,
        'content-type': 'multipart/form-data; boundary=boundary',
      })
      expect(response.status).toBe(content.length === body.length ? 200 : 400)
      expect(await readdir(temporaryDirectory)).toEqual([])
    }
    expect(execute).toHaveBeenCalledOnce()
  })

  it('accepts Go mime.FormatMediaType extended UTF-8 filenames', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    const filename = 'résumé.txt'
    const body = multipart('abc', filename)
      .toString()
      .replace(`filename="${filename}"`, `filename*=utf-8''${encodeURIComponent(filename)}`)
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        ...auth,
        'content-type': 'multipart/form-data; boundary=boundary',
      },
      body,
    })
    expect(response.status).toBe(200)
    expect(execute.mock.calls[0]?.[1][0]?.filename).toBe(filename)
  })

  it('enforces the 20-artifact cap without discarding excess parts', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    for (const count of [20, 21]) {
      const artifacts = Array.from({ length: count }, (_, index) => ({
        id: `a-${index}`,
        filename: 'a.txt',
        content_type: 'text/plain',
      }))
      const body = Buffer.concat([
        part('operation', JSON.stringify({ ...envelope(), artifacts })),
        ...artifacts.map(() => part('artifact', '', 'a.txt', 'text/plain')),
        Buffer.from('--boundary--\r\n'),
      ])
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'multipart/form-data; boundary=boundary' },
        body,
      })
      expect(response.status).toBe(count === 20 ? 200 : 400)
    }
    expect(execute).toHaveBeenCalledOnce()
  })

  it('rejects actual temporary-byte exhaustion and reclaims partial files', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url, temporaryDirectory } = await start(execute, { maxTemporaryBytes: 2 })
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'multipart/form-data; boundary=boundary' },
      body: multipart(),
    })
    expect(response.status).toBe(503)
    expect(execute).not.toHaveBeenCalled()
    expect(await readdir(temporaryDirectory)).toEqual([])
  })

  it('bounds slow/oversized requests and dangerous boundaries before mutation', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port, url } = await start(execute, { maxDurationMs: 50, maxRequestBytes: 1024 })
    expect(
      (
        await incompleteRequest(port, operationsRoute, {
          ...auth,
          'content-type': 'application/json',
          'transfer-encoding': 'chunked',
        })
      ).status,
    ).toBe(408)
    const oversized = await streamedRequest(port, operationsRoute, ['a'.repeat(2000)], {
      ...auth,
      'content-type': 'application/json',
    })
    expect(oversized.status).toBe(400)
    const boundary = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': `multipart/form-data; boundary=${'a'.repeat(252)}` },
      body: 'a',
    })
    expect(boundary.status).toBe(400)
    expect(execute).not.toHaveBeenCalled()
  })

  it('returns explicit failed/unknown results without leaking callback diagnostics or retrying', async () => {
    for (const error of [
      new Error(credential),
      new OperationRetryError('permanent_failure', false, 1),
    ]) {
      const execute = vi.fn<OperationsOptions['execute']>(() => Promise.reject(error))
      const { url, logger } = await start(execute)
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body: JSON.stringify(envelope()),
      })
      expect(await response.json()).toEqual({
        request_id: 'request-1',
        outcome: error instanceof OperationRetryError ? 'failed' : 'unknown',
      })
      expect(execute).toHaveBeenCalledOnce()
      expect(logger.error).not.toHaveBeenCalled()
    }
  })

  it('aborts provider I/O at the deadline, preserving unknown mutation outcome', async () => {
    let ioSignal: AbortSignal | undefined
    const execute = vi.fn<OperationsOptions['execute']>((_operation, _artifacts, signal) => {
      ioSignal = signal
      return new Promise((_resolve, reject) => {
        signal.addEventListener(
          'abort',
          () => {
            reject(new Error(credential))
          },
          { once: true },
        )
      })
    })
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'application/json' },
      body: JSON.stringify({ ...envelope(), deadline: new Date(Date.now() + 100).toISOString() }),
    })
    expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'unknown' })
    expect(ioSignal?.aborted).toBe(true)
    expect(execute).toHaveBeenCalledOnce()
  })

  it.each(['disconnect', 'shutdown'])(
    'aborts execution and cleans artifact files on %s',
    async (event) => {
      const started = deferred()
      const aborted = deferred()
      const execute = vi.fn<OperationsOptions['execute']>((_operation, _artifacts, signal) => {
        started.resolve()
        return new Promise((_resolve, reject) => {
          signal.addEventListener(
            'abort',
            () => {
              aborted.resolve()
              reject(new Error('stopped'))
            },
            { once: true },
          )
        })
      })
      const { url, server, temporaryDirectory } = await start(execute)
      const controller = new AbortController()
      const response = fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'multipart/form-data; boundary=boundary' },
        body: multipart(),
        signal: controller.signal,
      }).catch(() => undefined)
      await started.promise
      if (event === 'disconnect') controller.abort()
      else await server.close()
      await aborted.promise
      await response
      await vi.waitFor(async () => {
        expect(await readdir(temporaryDirectory)).toEqual([])
      })
    },
  )

  it('retains admission for a callback ignoring cancellation instead of admitting unbounded work', async () => {
    const settled = deferred()
    const execute = vi.fn<OperationsOptions['execute']>(async () => {
      await settled.promise
      return { outcome: 'failed' }
    })
    const { url, workBudget } = await start(execute, {
      maxConcurrentRequests: 1,
      maxDurationMs: 30,
    })
    try {
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body: JSON.stringify(envelope()),
      })
      expect(await response.json()).toMatchObject({ outcome: 'unknown' })
      expect(workBudget.usedBytes).toBe(operationWorkBytes)
      const second = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body: JSON.stringify(envelope()),
      })
      expect(second.status).toBe(503)
      expect(execute).toHaveBeenCalledOnce()
    } finally {
      settled.resolve()
    }
    await vi.waitFor(() => {
      expect(workBudget.usedBytes).toBe(0)
    })
  })

  it('rejects ambiguous deployment capabilities at construction', () => {
    expect(
      () =>
        new OperationsHandler({
          credential,
          allowedCapabilities: [capability, capability],
          execute: completed,
          maxConcurrentRequests: 1,
          maxDurationMs: 1000,
          maxTemporaryBytes: 100,
          maxRequestBytes: 100,
          temporaryDirectory: '/tmp',
          workBudget: new WorkByteBudget(operationWorkBytes),
        }),
    ).toThrow('invalid channel operation')
  })

  it('charges temporary bytes across concurrent requests until their files are removed', async () => {
    const started = deferred()
    const release = deferred()
    const execute = vi.fn<OperationsOptions['execute']>(async () => {
      started.resolve()
      await release.promise
      return { outcome: 'completed', payload: {} }
    })
    const { url, temporaryDirectory } = await start(execute, { maxTemporaryBytes: 3 })
    const send = () =>
      fetch(url, {
        method: 'POST',
        headers: {
          ...auth,
          'content-type': 'multipart/form-data; boundary=boundary',
        },
        body: multipart('ab'),
      })
    const first = send()
    await started.promise
    try {
      expect((await send()).status).toBe(503)
    } finally {
      release.resolve()
    }
    expect((await first).status).toBe(200)
    expect(await readdir(temporaryDirectory)).toEqual([])
    expect((await send()).status).toBe(200)
    expect(execute).toHaveBeenCalledTimes(2)
  })

  it('cleans a partial upload on connection loss before executing', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port, temporaryDirectory, workBudget } = await start(execute)
    const request = httpRequest({
      host: '127.0.0.1',
      port,
      path: operationsRoute,
      method: 'POST',
      headers: {
        ...auth,
        'content-type': 'multipart/form-data; boundary=boundary',
        'transfer-encoding': 'chunked',
      },
    })
    request.on('error', () => undefined)
    request.write(multipart().subarray(0, -16))
    try {
      await vi.waitFor(async () => {
        expect(await readdir(temporaryDirectory)).toHaveLength(1)
      })
    } finally {
      request.destroy()
    }
    await vi.waitFor(async () => {
      expect(await readdir(temporaryDirectory)).toEqual([])
      expect(workBudget.usedBytes).toBe(0)
    })
    expect(execute).not.toHaveBeenCalled()
  })

  it('waits for finite HTTP input even after the closing multipart boundary', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port, temporaryDirectory } = await start(execute, { maxDurationMs: 100 })
    const result = await new Promise<string>((resolve, reject) => {
      const request = httpRequest(
        {
          host: '127.0.0.1',
          port,
          path: operationsRoute,
          method: 'POST',
          headers: {
            ...auth,
            'content-type': 'multipart/form-data; boundary=boundary',
            'transfer-encoding': 'chunked',
          },
        },
        (response) => {
          let body = ''
          response.setEncoding('utf8')
          response.on('data', (chunk: string) => {
            body += chunk
          })
          response.on('end', () => {
            resolve(body)
          })
        },
      )
      request.on('error', reject)
      request.write(multipart()) // Deliberately never end the HTTP request.
    })
    expect(result).toContain('"outcome":"failed"')
    expect(execute).not.toHaveBeenCalled()
    expect(await readdir(temporaryDirectory)).toEqual([])
  })

  it('rejects an oversized header instead of trusting its truncated prefix', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    const body = multipart()
      .toString()
      .replace(
        'Content-Type: text/plain\r\n',
        `Content-Type: text/plain\r\n${'a'.repeat(16_384)}\r\n`,
      )
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        ...auth,
        'content-type': 'multipart/form-data; boundary=boundary',
      },
      body,
    })
    expect(response.status).toBe(400)
    expect(execute).not.toHaveBeenCalled()
  })

  it.each([
    'X-Trace: ignored',
    'Broken header',
    'Broken header\r\nContent-Disposition: form-data; name="other"; filename="other.txt"',
    'Broken header\r\nContent-Type: application/json\r\nRequest-Id: another-request',
  ])(
    'tolerates ignored MIME suffixes without changing authorized artifacts or correlation (%s)',
    async (extra) => {
      const execute = vi.fn<OperationsOptions['execute']>(async (operation, artifacts) => {
        expect(operation.requestId).toBe('request-1')
        expect(operation.scope).toEqual(envelope().scope)
        expect(artifacts).toHaveLength(1)
        const artifact = artifacts[0]
        expect(artifact).toMatchObject({
          id: 'artifact-1',
          filename: 'a.txt',
          content_type: 'text/plain',
          sizeBytes: 3,
        })
        let bytes = ''
        if (!artifact) throw new Error('missing artifact')
        for await (const chunk of artifact.open()) bytes += String(chunk)
        expect(bytes).toBe('abc')
        return completed()
      })
      const response = await receiveMultipart(
        execute,
        Buffer.from(
          multipart()
            .toString()
            .replace('Content-Type: text/plain\r\n', `Content-Type: text/plain\r\n${extra}\r\n`),
        ),
      )
      expect(response.status).toBe(200)
      expect(await response.json()).toMatchObject({ request_id: 'request-1', outcome: 'completed' })
      expect(execute).toHaveBeenCalledOnce()
    },
  )

  it('does not let ignored MIME suffixes hide an extra artifact or alter selected headers', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    for (const body of [
      multipart().toString().replace('name="artifact"', 'name="other"'),
      multipart().toString().replace('filename="a.txt"', 'filename="other.txt"'),
      multipart().toString().replace('Content-Type: text/plain', 'Content-Type: application/json'),
      multipart()
        .toString()
        .replace(
          '--boundary--\r\n',
          `${part('artifact', 'extra', 'a.txt', 'text/plain').toString()}--boundary--\r\n`,
        ),
    ]) {
      const suffixed = body.replace(
        'Content-Type: application/json\r\n',
        'Content-Type: application/json\r\nBroken header\r\n',
      )
      expect((await receiveMultipart(execute, Buffer.from(suffixed))).status).toBe(400)
    }
    expect(execute).not.toHaveBeenCalled()
  })

  it('returns known failure for an already-expired operation without invoking the callback', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { url } = await start(execute)
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...auth, 'content-type': 'application/json' },
      body: JSON.stringify({ ...envelope(), deadline: '2020-01-01T00:00:00Z' }),
    })
    expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'failed' })
    expect(execute).not.toHaveBeenCalled()
  })

  it('rejects invalid or excessive callback payloads with an honest unknown outcome', async () => {
    for (const payload of [[], { value: 'a'.repeat(1024 * 1024) }]) {
      const execute = vi.fn<OperationsOptions['execute']>(() =>
        Promise.resolve({ outcome: 'completed', payload }),
      )
      const { url } = await start(execute)
      const response = await fetch(url, {
        method: 'POST',
        headers: { ...auth, 'content-type': 'application/json' },
        body: JSON.stringify(envelope()),
      })
      expect(await response.json()).toEqual({ request_id: 'request-1', outcome: 'unknown' })
      expect(execute).toHaveBeenCalledOnce()
    }
  })
})
import { execFile } from 'node:child_process'
