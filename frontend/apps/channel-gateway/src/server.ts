import { once } from 'node:events'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

import { getRequestListener, type HttpBindings, RequestError } from '@hono/node-server'
import { type Context, Hono } from 'hono'

import type { AppRuntimeRegistry, RuntimeHandle } from './app-registry'
import { isCoreNotFoundError } from './core-client'
import { errorMessage } from './diagnostics'
import {
  abortError,
  BackgroundTaskTracker,
  BodyTooLargeError,
  declaredBodyExceedsLimit,
  providerResponseHeaders,
  raceWithAbort,
  readBody,
  readProviderResponseBody,
} from './server-support'
import type { GatewayLogger } from './types'
import { GatewayAtCapacityError, type WorkByteBudget } from './work-budget'

interface GatewayEnv {
  Bindings: HttpBindings
}

const webhookRoute =
  '/hooks/:integrationAppId{iapp_[a-z2-7]{26}}/:provider{[a-z0-9][a-z0-9_.-]{0,127}}/*'
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
  private readonly activeRequests = new Map<AbortController, Promise<void>>()
  private backgroundFailures = 0
  private rejectedRequests = 0
  private requestCount = 0
  private server?: Server

  constructor(private readonly options: GatewayServerOptions) {}

  async listen(): Promise<number> {
    if (this.server) throw new Error('channel gateway server is already listening')
    const listener = getRequestListener(this.createApp().fetch, {
      // We own bounded body consumption and cleanup of incomplete requests.
      autoCleanupIncoming: false,
      hostname: 'channel-gateway.internal',
      // Native Fetch constructors keep provider body-copy accounting stable.
      overrideGlobalObjects: false,
      errorHandler: (error) => {
        const malformed = error instanceof RequestError
        if (malformed) {
          this.options.logger.warn('rejected malformed HTTP request', {
            error: errorMessage(error),
          })
        } else {
          this.logRequestError(error)
        }
        return new Response(malformed ? 'bad request' : 'internal server error', {
          status: malformed ? 400 : 500,
          headers: { connection: 'close', 'content-type': 'text/plain; charset=utf-8' },
        })
      },
    })
    const server = createServer((request, response) => {
      void listener(request, response)
    })
    server.headersTimeout = this.options.handlerTimeoutMs
    server.requestTimeout = this.options.handlerTimeoutMs
    server.keepAliveTimeout = Math.min(this.options.handlerTimeoutMs, 5_000)
    server.maxConnections = this.options.maxConcurrentRequests + 16
    server.maxRequestsPerSocket = 1_000
    this.server = server
    try {
      server.listen(this.options.port, '0.0.0.0')
      await once(server, 'listening')
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
    const requests = Promise.allSettled([...this.activeRequests.values()])
    const completed = Promise.all([closed, requests]).then(() => true)
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
      for (const controller of this.activeRequests.keys()) {
        controller.abort(new Error('channel gateway is shutting down'))
      }
      server.closeAllConnections()
      this.options.logger.warn('channel gateway HTTP shutdown reached its deadline')
      await completed
    }
  }

  private createApp(): Hono<GatewayEnv> {
    // Match canonical provider/app identifiers without Hono's default path
    // decoding. The original path and query also reach signature verification.
    const app = new Hono<GatewayEnv>({ getPath: (request) => new URL(request.url).pathname })
    app.use(async (context, next) => {
      try {
        await next()
      } finally {
        closeIncompleteRequest(context.env.incoming, context.env.outgoing)
      }
    })
    app.all('/healthz', (context) =>
      context.json({ ok: true }, 200, { 'cache-control': 'no-store' }),
    )
    app.all('/readyz', async (context) => {
      let ready = false
      try {
        ready = (await this.options.isReady?.()) ?? true
      } catch (error) {
        this.options.logger.warn('channel gateway readiness check failed', {
          error: errorMessage(error),
        })
      }
      return context.json({ ok: ready }, ready ? 200 : 503, { 'cache-control': 'no-store' })
    })
    app.all('/metrics', (context) =>
      context.text(this.metricsText(), 200, {
        'content-type': 'text/plain; version=0.0.4; charset=utf-8',
      }),
    )
    app.all(webhookRoute, (context) =>
      this.handleWebhook(
        context,
        context.req.param('integrationAppId'),
        context.req.param('provider'),
      ),
    )
    app.notFound((context) => context.text('not found', 404))
    app.onError((error, context) => {
      this.logRequestError(error)
      return context.text('internal server error', 500)
    })
    return app
  }

  private async handleWebhook(
    context: Context<GatewayEnv>,
    integrationAppId: string,
    provider: string,
  ): Promise<Response> {
    const { incoming } = context.env
    this.requestCount += 1
    if (this.activeRequests.size >= this.options.maxConcurrentRequests) {
      this.rejectedRequests += 1
      return context.text('channel gateway is at capacity', 503, {
        connection: 'close',
        'retry-after': '1',
      })
    }

    const controller = new AbortController()
    const { signal } = controller
    // Register the whole lifetime before any await: shutdown must include
    // background work even when it starts after shutdown has begun.
    let finish!: () => void
    const finished = new Promise<void>((resolve) => {
      finish = resolve
    })
    this.activeRequests.set(controller, finished)
    const deadline = setTimeout(() => {
      controller.abort(new Error('channel webhook handler reached its deadline'))
    }, this.options.handlerTimeoutMs)
    const background = new BackgroundTaskTracker(this.options.workBudget.reserve)
    let handle: RuntimeHandle | undefined
    let releaseBody = (): void => undefined
    let drain: Promise<unknown[]> = Promise.resolve([])
    // Settle the request lifetime even if a cleanup step fails.
    const cleanup = (): Promise<void> =>
      Promise.resolve()
        .then(() => {
          background.close()
        })
        .finally(() => handle?.release())
        .finally(releaseBody)
        .finally(() => {
          clearTimeout(deadline)
          controller.abort(new Error('channel webhook request completed'))
          this.activeRequests.delete(controller)
          finish()
        })
    try {
      if (declaredBodyExceedsLimit(incoming, this.options.bodyLimitBytes)) {
        throw new BodyTooLargeError()
      }
      const acquisition = this.options.registry.acquire(integrationAppId)
      try {
        handle = await raceWithAbort(acquisition, signal)
      } catch (error) {
        if (signal.aborted) {
          // A shared registry load cannot be canceled by one timed-out request.
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
        this.options.logger.warn('load channel provider app', {
          error: errorMessage(error),
          integration_app_id: integrationAppId,
        })
        return isCoreNotFoundError(error)
          ? context.text('not found', 404)
          : context.text('channel provider is unavailable', 503, { 'retry-after': '1' })
      }
      if (handle.configuration.app.provider !== provider) {
        return context.text('not found', 404)
      }
      // Keep a single raw-body consumer. A generic body parser cannot account
      // for shared memory limits and the transient copies made below.
      const buffered = await readBody(incoming, this.options.bodyLimitBytes, signal, (bytes) =>
        this.options.workBudget.tryAdjust(bytes),
      )
      releaseBody = buffered.release
      const routeUrl = new URL(context.req.url)
      const providerUrl = new URL(`${routeUrl.pathname}${routeUrl.search}`, this.options.publicUrl)
      const copiesBody = context.req.method !== 'GET' && context.req.method !== 'HEAD'
      const requestCopyReservation =
        copiesBody && buffered.body.byteLength > 0
          ? this.options.workBudget.reserve(buffered.body.byteLength)
          : undefined
      if (requestCopyReservation) {
        releaseBody = () => {
          buffered.release()
          requestCopyReservation.release()
        }
      }
      const providerRequest = new Request(providerUrl, {
        body: copiesBody
          ? new Uint8Array(
              buffered.body.buffer as ArrayBuffer,
              buffered.body.byteOffset,
              buffered.body.byteLength,
            )
          : undefined,
        headers: context.req.raw.headers,
        method: context.req.method,
        signal,
      })
      const providerResponse = await raceWithAbort(
        handle.handleWebhook(providerRequest, background),
        signal,
      )
      const responseBody = await readProviderResponseBody(
        providerResponse,
        providerResponseBodyLimitBytes,
        signal,
      )
      const response = new Response(providerResponse.body === null ? null : responseBody, {
        headers: providerResponseHeaders(providerResponse.headers),
        status: providerResponse.status,
      })
      drain = background.drain(signal)
      return response
    } catch (error) {
      if (error instanceof BodyTooLargeError) {
        return context.text('request body too large', 413)
      }
      if (error instanceof GatewayAtCapacityError) {
        this.rejectedRequests += 1
        return context.text('channel gateway is at capacity', 503, {
          connection: 'close',
          'retry-after': '1',
        })
      }
      throw error
    } finally {
      // Send responses promptly, including rejections, while retaining the
      // runtime and reservations through background work and runtime release.
      void drain
        .then((failures) => {
          this.backgroundFailures += failures.length
          for (const error of failures) {
            this.options.logger.error('channel webhook background task failed', {
              error: errorMessage(error),
              integration_app_id: integrationAppId,
            })
          }
        })
        .finally(cleanup)
        .catch((error: unknown) => {
          this.logRequestError(error)
        })
    }
  }

  private logRequestError(error: unknown): void {
    this.options.logger.error('channel webhook request failed', { error: errorMessage(error) })
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
      `omnara_channel_gateway_active_webhook_requests ${this.activeRequests.size}`,
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
