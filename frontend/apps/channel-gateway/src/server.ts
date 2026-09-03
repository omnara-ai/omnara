import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

import type { AppRuntimeRegistry } from './app-registry'
import { isCoreNotFoundError } from './core-client'
import { errorMessage } from './diagnostics'
import {
  abortError,
  BackgroundTaskTracker,
  BodyTooLargeError,
  declaredBodyExceedsLimit,
  jsonStatus,
  providerResponseHeaders,
  raceWithAbort,
  readBody,
  readProviderResponseBody,
  textStatus,
} from './server-support'
import type { GatewayLogger } from './types'
import { GatewayAtCapacityError, type WorkByteBudget } from './work-budget'

const appIdPattern = /^iapp_[a-z2-7]{26}$/
const providerResponseBodyLimitBytes = 64 * 1024

export interface GatewayServerOptions {
  bodyLimitBytes: number
  handlerTimeoutMs: number
  httpShutdownTimeoutMs: number
  isReady?: () => boolean | Promise<boolean>
  logger: GatewayLogger
  maxConcurrentRequests: number
  port: number
  publicUrl: string
  registry: AppRuntimeRegistry
  workBudget: WorkByteBudget
}

export class GatewayServer {
  private readonly activeHandlers = new Set<Promise<void>>()
  private readonly activeRequests = new Set<AbortController>()
  private activeWebhookRequests = 0
  private backgroundFailures = 0
  private rejectedRequests = 0
  private requestCount = 0
  private server?: Server

  constructor(private readonly options: GatewayServerOptions) {}

  async listen(): Promise<number> {
    if (this.server) throw new Error('channel gateway server is already listening')
    const server = createServer((request, response) => {
      const work = this.handle(request, response)
        .catch((error: unknown) => {
          this.options.logger.error('channel webhook request failed', {
            error: errorMessage(error),
          })
          if (!response.headersSent) {
            closeIncompleteRequest(request, response)
            response.writeHead(500, { 'content-type': 'text/plain; charset=utf-8' })
          }
          if (!response.writableEnded) response.end('internal server error')
        })
        .finally(() => {
          this.activeHandlers.delete(work)
        })
      this.activeHandlers.add(work)
    })
    server.headersTimeout = this.options.handlerTimeoutMs
    server.requestTimeout = this.options.handlerTimeoutMs
    server.keepAliveTimeout = Math.min(this.options.handlerTimeoutMs, 5_000)
    server.maxConnections = this.options.maxConcurrentRequests + 16
    server.maxRequestsPerSocket = 1_000
    this.server = server
    try {
      await new Promise<void>((resolve, reject) => {
        const cleanup = (): void => {
          server.removeListener('error', onError)
          server.removeListener('listening', onListening)
        }
        const onError = (error: Error): void => {
          cleanup()
          reject(error)
        }
        const onListening = (): void => {
          cleanup()
          resolve()
        }
        server.once('error', onError)
        server.once('listening', onListening)
        server.listen(this.options.port, '0.0.0.0')
      })
    } catch (error) {
      this.server = undefined
      throw error
    }
    const address = server.address()
    if (!address || typeof address === 'string') {
      await this.close()
      throw new Error('channel gateway did not bind a TCP port')
    }
    return address.port
  }

  async close(): Promise<void> {
    const server = this.server
    this.server = undefined
    if (!server) return
    const closed = new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error) reject(error)
        else resolve()
      })
    })
    const handlers = Promise.allSettled([...this.activeHandlers]).then(() => undefined)
    const completed = Promise.all([closed, handlers]).then(() => true)
    let shutdownTimer: ReturnType<typeof setTimeout> | undefined
    const deadline = new Promise<false>((resolve) => {
      shutdownTimer = setTimeout(() => {
        resolve(false)
      }, this.options.httpShutdownTimeoutMs)
    })
    let withinDeadline: boolean
    try {
      withinDeadline = await Promise.race([completed, deadline])
    } finally {
      if (shutdownTimer) clearTimeout(shutdownTimer)
    }
    if (!withinDeadline) {
      for (const controller of this.activeRequests) {
        controller.abort(new Error('channel gateway is shutting down'))
      }
      server.closeAllConnections()
      this.options.logger.warn('channel gateway HTTP shutdown reached its deadline')
      await completed
    }
  }

  private async handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    const routeUrl = new URL(request.url ?? '/', 'http://channel-gateway.internal')
    if (routeUrl.pathname === '/healthz') {
      jsonStatus(response, 200, '{"ok":true}')
      return
    }
    if (routeUrl.pathname === '/readyz') {
      let ready = false
      try {
        ready = (await this.options.isReady?.()) ?? true
      } catch (error) {
        this.options.logger.warn('channel gateway readiness check failed', {
          error: errorMessage(error),
        })
      }
      jsonStatus(response, ready ? 200 : 503, `{"ok":${ready ? 'true' : 'false'}}`)
      return
    }
    if (routeUrl.pathname === '/metrics') {
      response.writeHead(200, { 'content-type': 'text/plain; version=0.0.4; charset=utf-8' })
      response.end(this.metricsText())
      return
    }

    const match = /^\/hooks\/([^/]+)\/([^/]+)(?:\/.*)?$/.exec(routeUrl.pathname)
    if (!match) {
      textStatus(response, 404, 'not found')
      return
    }
    const integrationAppId = match[1]
    const provider = match[2]
    if (!integrationAppId || !provider || !appIdPattern.test(integrationAppId)) {
      textStatus(response, 404, 'not found')
      return
    }
    this.requestCount += 1
    if (this.activeWebhookRequests >= this.options.maxConcurrentRequests) {
      this.rejectedRequests += 1
      closeIncompleteRequest(request, response)
      response.writeHead(503, {
        connection: 'close',
        'content-type': 'text/plain; charset=utf-8',
        'retry-after': '1',
      })
      response.end('channel gateway is at capacity')
      return
    }
    this.activeWebhookRequests += 1
    const requestController = new AbortController()
    const deadline = setTimeout(() => {
      requestController.abort(new Error('channel webhook handler reached its deadline'))
    }, this.options.handlerTimeoutMs)
    this.activeRequests.add(requestController)
    let releaseBody = (): void => undefined
    try {
      await this.handleWebhook(
        request,
        response,
        routeUrl,
        integrationAppId,
        provider,
        requestController.signal,
        (release) => {
          releaseBody = release
        },
      )
    } finally {
      releaseBody()
      clearTimeout(deadline)
      this.activeRequests.delete(requestController)
      requestController.abort(new Error('channel webhook request completed'))
      this.activeWebhookRequests -= 1
    }
  }

  private async handleWebhook(
    request: IncomingMessage,
    response: ServerResponse,
    routeUrl: URL,
    integrationAppId: string,
    provider: string,
    signal: AbortSignal,
    retainBody: (release: () => void) => void,
  ): Promise<void> {
    if (declaredBodyExceedsLimit(request, this.options.bodyLimitBytes)) {
      closeIncompleteRequest(request, response)
      textStatus(response, 413, 'request body too large')
      return
    }
    let handle
    const acquisition = this.options.registry.acquire(integrationAppId)
    try {
      handle = await raceWithAbort(acquisition, signal)
    } catch (error) {
      if (signal.aborted) {
        // The registry cannot cancel a shared, coalesced load. If it succeeds
        // after this request has timed out, release the acquired reference.
        void acquisition
          .then(
            (lateHandle) => lateHandle.release(),
            () => undefined,
          )
          .catch((releaseError: unknown) => {
            this.options.logger.error('release late channel provider app acquisition', {
              error: errorMessage(releaseError),
              integration_app_id: integrationAppId,
            })
          })
        throw abortError(signal)
      }
      closeIncompleteRequest(request, response)
      this.options.logger.warn('load channel provider app', {
        error: errorMessage(error),
        integration_app_id: integrationAppId,
      })
      if (isCoreNotFoundError(error)) {
        textStatus(response, 404, 'not found')
      } else {
        response.writeHead(503, {
          'content-type': 'text/plain; charset=utf-8',
          'retry-after': '1',
        })
        response.end('channel provider is unavailable')
      }
      return
    }
    try {
      if (handle.configuration.app.provider !== provider) {
        closeIncompleteRequest(request, response)
        textStatus(response, 404, 'not found')
        return
      }
      const buffered = await readBody(request, this.options.bodyLimitBytes, signal, (bytes) =>
        this.options.workBudget.tryAdjust(bytes),
      )
      retainBody(() => {
        buffered.release()
      })
      const headers = new Headers()
      for (let index = 0; index < request.rawHeaders.length; index += 2) {
        const name = request.rawHeaders[index]
        const value = request.rawHeaders[index + 1]
        if (name !== undefined && value !== undefined) headers.append(name, value)
      }
      const providerUrl = new URL(`${routeUrl.pathname}${routeUrl.search}`, this.options.publicUrl)
      const copiesBody = request.method !== 'GET' && request.method !== 'HEAD'
      const requestCopyReservation =
        copiesBody && buffered.body.byteLength > 0
          ? this.options.workBudget.reserve(buffered.body.byteLength)
          : undefined
      if (requestCopyReservation) {
        retainBody(() => {
          buffered.release()
          requestCopyReservation.release()
        })
      }
      const providerRequest = new Request(providerUrl, {
        body: copiesBody
          ? new Uint8Array(
              buffered.body.buffer as ArrayBuffer,
              buffered.body.byteOffset,
              buffered.body.byteLength,
            )
          : undefined,
        headers,
        method: request.method,
        signal,
      })
      const background = new BackgroundTaskTracker(this.options.workBudget.reserve)
      try {
        const providerResponse = await raceWithAbort(
          handle.handleWebhook(providerRequest, background),
          signal,
        )
        const responseBody = await readProviderResponseBody(
          providerResponse,
          providerResponseBodyLimitBytes,
          signal,
        )
        response.writeHead(
          providerResponse.status,
          providerResponseHeaders(providerResponse.headers),
        )
        response.end(responseBody)

        const failures = await background.drain(signal)
        if (failures.length > 0) {
          this.backgroundFailures += failures.length
          for (const error of failures) {
            this.options.logger.error('channel webhook background task failed', {
              error: errorMessage(error),
              integration_app_id: integrationAppId,
            })
          }
        }
      } finally {
        background.close()
      }
    } catch (error) {
      if (error instanceof BodyTooLargeError) {
        closeIncompleteRequest(request, response)
        textStatus(response, 413, 'request body too large')
        return
      }
      if (error instanceof GatewayAtCapacityError) {
        this.rejectedRequests += 1
        closeIncompleteRequest(request, response)
        response.writeHead(503, {
          connection: 'close',
          'content-type': 'text/plain; charset=utf-8',
          'retry-after': '1',
        })
        response.end('channel gateway is at capacity')
        return
      }
      throw error
    } finally {
      await handle.release()
    }
  }

  private metricsText(): string {
    return [
      '# TYPE omnara_channel_gateway_webhook_requests_total counter',
      `omnara_channel_gateway_webhook_requests_total ${this.requestCount}`,
      '# TYPE omnara_channel_gateway_rejected_requests_total counter',
      `omnara_channel_gateway_rejected_requests_total ${this.rejectedRequests}`,
      '# TYPE omnara_channel_gateway_background_failures_total counter',
      `omnara_channel_gateway_background_failures_total ${this.backgroundFailures}`,
      '# TYPE omnara_channel_gateway_active_webhook_requests gauge',
      `omnara_channel_gateway_active_webhook_requests ${this.activeWebhookRequests}`,
      '# TYPE omnara_channel_gateway_buffered_work_bytes gauge',
      `omnara_channel_gateway_buffered_work_bytes ${this.options.workBudget.usedBytes}`,
      '',
    ].join('\n')
  }
}

function closeIncompleteRequest(request: IncomingMessage, response: ServerResponse): void {
  if (request.complete || request.destroyed) return
  request.pause()
  response.shouldKeepAlive = false
  response.setHeader('connection', 'close')
  response.once('finish', () => {
    request.destroy()
  })
}
