import { once } from 'node:events'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

import { getRequestListener, type HttpBindings, RequestError } from '@hono/node-server'
import { type Context, Hono } from 'hono'

import { abortError, raceWithAbort } from '../async'
import { isCoreNotFoundError } from '../core/requests'
import { errorMessage, isString } from '../diagnostics'
import {
  BodyTooLargeError,
  declaredBodyExceedsLimit,
  providerResponseHeaders,
  readBody,
  readProviderResponseBody,
} from '../http-io'
import { OperationsHandler, type OperationsOptions, operationsRoute } from '../operations/handler'
import type { AppRuntimeRegistry, RuntimeHandle } from '../runtime/registry'
import type { GatewayLogger } from '../types'
import { GatewayAtCapacityError, type WorkByteBudget, WorkReservationScope } from '../work-budget'

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
  operations?: Omit<OperationsOptions, 'workBudget' | 'logger'>
  port: number
  publicUrl: string
  registry?: Pick<AppRuntimeRegistry, 'acquire' | 'webhookTimeoutMs' | 'webhookBodyLimitBytes'>
  workBudget: WorkByteBudget
}

export class GatewayServer {
  private readonly activeRequests = new Map<AbortController, Promise<void>>()
  private rejectedRequests = 0
  private requestCount = 0
  private server?: Server
  private readonly operations?: OperationsHandler

  constructor(private readonly options: GatewayServerOptions) {
    if (options.operations) {
      this.operations = new OperationsHandler({
        ...options.operations,
        logger: options.logger,
        workBudget: options.workBudget,
      })
    }
  }

  async listen(): Promise<number> {
    if (this.server) throw new Error('channel gateway server is already listening')
    const listener = getRequestListener(this.createApp().fetch, {
      // The adapter bounds discarded request bodies after responding. Destroying
      // an incomplete request on response finish can reset an unreceived reply.
      autoCleanupIncoming: true,
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
    server.requestTimeout = Math.max(
      this.options.handlerTimeoutMs,
      this.options.operations?.maxDurationMs ?? 0,
    )
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
    if (!address || isString(address)) {
      await this.close()
      throw new Error('channel gateway did not bind a TCP port')
    }
    return address.port
  }

  async close(): Promise<void> {
    const operationsClosed = this.operations?.close(this.options.httpShutdownTimeoutMs)
    const server = this.server
    this.server = undefined
    if (!server) {
      await operationsClosed
      return
    }
    const closed = new Promise<void>((resolve, reject) => {
      server.close((error) => {
        if (error) reject(error)
        else resolve()
      })
    })
    const requests = Promise.allSettled(this.activeRequests.values())
    const completed = Promise.all([closed, requests, operationsClosed]).then(() => true)
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
      // Actual handlers retain their admission/memory until settlement, but an
      // uncooperative provider must not keep process shutdown waiting forever.
      await closed
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
    app.all(operationsRoute, (context) => {
      if (!this.operations) return context.text('not found', 404)
      if (context.req.method !== 'POST') return context.text('method not allowed', 405)
      return this.operations.handle(context.env.incoming, context.env.outgoing)
    })
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
    const registry = this.options.registry
    if (!registry) return context.text('not found', 404)
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
    // Register before any await; the lifetime includes actual handler settlement
    // and runtime release, even if the HTTP response has already timed out.
    let finish!: () => void
    const finished = new Promise<void>((resolve) => {
      finish = resolve
    })
    this.activeRequests.set(controller, finished)
    const deadline = setTimeout(
      () => {
        controller.abort(new Error('channel webhook handler reached its deadline'))
      },
      Math.min(this.options.handlerTimeoutMs, registry.webhookTimeoutMs(provider) ?? Infinity),
    )
    const work = new WorkReservationScope(this.options.workBudget.reserve)
    let handle: RuntimeHandle | undefined
    let releaseBody = (): void => undefined
    let handler: Promise<Response> | undefined
    let providerResponse: Response | undefined
    // Settle the request lifetime even if a cleanup step fails.
    const cleanup = (): Promise<void> =>
      Promise.resolve()
        .then(() => {
          work.close()
        })
        // Promise.finally awaits cleanup promises; the lib type is () => void.
        // oxlint-disable-next-line typescript/no-misused-promises
        .finally(() => handle?.release())
        .finally(releaseBody)
        .finally(() => {
          this.activeRequests.delete(controller)
          finish()
        })
    try {
      if (declaredBodyExceedsLimit(incoming, this.options.bodyLimitBytes)) {
        throw new BodyTooLargeError()
      }
      const acquisition = registry.acquire(integrationAppId)
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
            .catch((cause: unknown) => {
              this.options.logger.error('release late channel provider app acquisition', {
                error: errorMessage(cause),
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
      const bodyLimitBytes = Math.min(
        this.options.bodyLimitBytes,
        registry.webhookBodyLimitBytes(handle.configuration.app.connector_key, provider) ??
          Infinity,
      )
      if (declaredBodyExceedsLimit(incoming, bodyLimitBytes)) throw new BodyTooLargeError()
      // Keep a single raw-body consumer. A generic body parser cannot account
      // for shared memory limits and the transient copies made below.
      const buffered = await readBody(incoming, bodyLimitBytes, signal, (bytes) =>
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
      // SAFETY: the body contains only Node IncomingMessage chunks or Buffer.concat
      // output, both backed by ArrayBuffer (never caller-supplied shared memory).
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
      handler = handle.handleWebhook(providerRequest, { reserveWorkBytes: work.reserve })
      providerResponse = await raceWithAbort(handler, signal)
      const responseBody = await readProviderResponseBody(
        providerResponse,
        providerResponseBodyLimitBytes,
        signal,
      )
      const response = new Response(providerResponse.body === null ? null : responseBody, {
        headers: providerResponseHeaders(providerResponse.headers),
        status: providerResponse.status,
      })
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
      clearTimeout(deadline)
      controller.abort(new Error('channel webhook request completed'))
      // Promptly finish HTTP while retaining actual work. A response arriving
      // after cancellation has no consumer; discard it without reading its body.
      const settled =
        handler?.then(
          (response) => {
            if (response !== providerResponse) void response.body?.cancel().catch(() => undefined)
          },
          () => undefined, // Observe late rejection; the HTTP failure is already reported.
        ) ?? Promise.resolve()
      void settled
        // Retain the request lifetime until asynchronous cleanup has finished.
        // oxlint-disable-next-line typescript/no-misused-promises
        .finally(cleanup)
        .catch((cause: unknown) => {
          this.logRequestError(cause)
        })
    }
  }

  private logRequestError(cause: unknown): void {
    this.options.logger.error('channel webhook request failed', { error: errorMessage(cause) })
  }

  private metricsText(): string {
    return [
      '# TYPE omnara_channel_gateway_webhook_requests_total counter',
      `omnara_channel_gateway_webhook_requests_total ${this.requestCount}`,
      '# TYPE omnara_channel_gateway_rejected_requests_total counter',
      `omnara_channel_gateway_rejected_requests_total ${this.rejectedRequests}`,
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
  // Only GET/HEAD skip the adapter's bounded body drain. Closing a POST during
  // its upload can make the client's write error win over our rejection reply.
  if (request.method !== 'GET' && request.method !== 'HEAD') return
  response.shouldKeepAlive = false
  response.setHeader('connection', 'close')
}
