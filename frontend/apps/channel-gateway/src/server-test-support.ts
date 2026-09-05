import { request as httpRequest } from 'node:http'

import { vi } from 'vitest'

import type { AppRuntimeRegistry } from './app-registry'
import { GatewayServer } from './server'
import type { GatewayLogger, ProviderRuntime } from './types'
import { WorkByteBudget } from './work-budget'

export const integrationAppId = 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa'

export async function startServer(
  runtime: ProviderRuntime,
  overrides: {
    acquireGate?: Promise<void>
    acquireError?: Error
    bodyLimitBytes?: number
    handlerTimeoutMs?: number
    httpShutdownTimeoutMs?: number
    isReady?: () => boolean | Promise<boolean>
    maxBufferedWorkBytes?: number
    maxConcurrentRequests?: number
    provider?: string
    releaseGate?: Promise<void>
  } = {},
): Promise<{
  logger: ReturnType<typeof testLogger>
  port: number
  registry: { acquire: ReturnType<typeof vi.fn> }
  release: ReturnType<typeof vi.fn>
  server: GatewayServer
}> {
  const release = vi.fn(() => overrides.releaseGate ?? Promise.resolve())
  const registry = {
    acquire: vi.fn(async () => {
      if (overrides.acquireError) return Promise.reject(overrides.acquireError)
      await overrides.acquireGate
      return Promise.resolve({
        configuration: { app: { provider: overrides.provider ?? 'discord' } },
        handleWebhook: (
          request: Request,
          context: Parameters<ProviderRuntime['handleWebhook']>[1],
        ) => runtime.handleWebhook(request, context),
        release,
        runtime,
      })
    }),
  }
  const logger = testLogger()
  const server = new GatewayServer({
    bodyLimitBytes: overrides.bodyLimitBytes ?? 1024,
    handlerTimeoutMs: overrides.handlerTimeoutMs ?? 1_000,
    httpShutdownTimeoutMs: overrides.httpShutdownTimeoutMs ?? 100,
    isReady: overrides.isReady,
    logger,
    maxConcurrentRequests: overrides.maxConcurrentRequests ?? 8,
    port: 0,
    publicUrl: 'https://channels.example.test',
    registry: registry as unknown as AppRuntimeRegistry,
    workBudget: new WorkByteBudget(overrides.maxBufferedWorkBytes ?? 2048),
  })
  const port = await server.listen()
  return { logger, port, registry, release, server }
}

export function providerRuntime(
  handleWebhook: ProviderRuntime['handleWebhook'] = () => Promise.resolve(new Response()),
): ProviderRuntime {
  return {
    close: () => Promise.resolve(),
    handleWebhook,
    send: () => Promise.resolve({ providerMessageRef: '' }),
  }
}

export async function streamedRequest(
  port: number,
  path: string,
  chunks: string[],
  headers: Record<string, string> = {},
): Promise<{ body: string; status: number }> {
  return new Promise((resolve, reject) => {
    const request = httpRequest(
      {
        headers: { 'transfer-encoding': 'chunked', ...headers },
        host: '127.0.0.1',
        method: 'POST',
        path,
        port,
      },
      (response) => {
        let body = ''
        response.setEncoding('utf8')
        response.on('data', (chunk: string) => {
          body += chunk
        })
        response.on('end', () => {
          resolve({ body, status: response.statusCode ?? 0 })
        })
      },
    )
    request.on('error', reject)
    for (const chunk of chunks) request.write(chunk)
    request.end()
  })
}

export async function incompleteRequest(
  port: number,
  path: string,
  headers: Record<string, string>,
): Promise<{ connection: string | undefined; status: number }> {
  return new Promise((resolve, reject) => {
    let response: { connection: string | undefined; status: number } | undefined
    const request = httpRequest(
      { headers, host: '127.0.0.1', method: 'POST', path, port },
      (incoming) => {
        response = {
          connection: incoming.headers.connection,
          status: incoming.statusCode ?? 0,
        }
        incoming.resume()
      },
    )
    const timeout = setTimeout(() => {
      request.destroy()
      reject(new Error('incomplete request connection was not closed'))
    }, 1_000)
    request.on('close', () => {
      clearTimeout(timeout)
      if (response) resolve(response)
      else reject(new Error('incomplete request closed before receiving a response'))
    })
    request.on('error', (error) => {
      if (!response) {
        clearTimeout(timeout)
        reject(error)
      }
    })
    request.write('a')
  })
}

function testLogger() {
  return {
    debug: vi.fn<GatewayLogger['debug']>(),
    error: vi.fn<GatewayLogger['error']>(),
    info: vi.fn<GatewayLogger['info']>(),
    warn: vi.fn<GatewayLogger['warn']>(),
  }
}

export function deferred(): { promise: Promise<void>; resolve(): void } {
  let resolve!: () => void
  const promise = new Promise<void>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}
