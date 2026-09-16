import { describe, expect, it, vi } from 'vitest'

import { AppRuntimeRegistry, type RuntimeHandle } from './app-registry'
import { CoreClient } from './core-client'
import { unexpectedTestCall } from './gateway-test-fixtures'
import { createGitHubFactory } from './github/factory'
import { app, contexts, coreFixture, signedRequest } from './github/inbound-test-support'
import { localServer } from './github/test-support'
import { GatewayServer } from './server'
import { deferred, incompleteRequest } from './server-test-support'
import { providerFactoryKey } from './types'

const slowAppId = 'iapp_bbbbbbbbbbbbbbbbbbbbbbbbbb'

async function fixture(options: { appGate?: Promise<void>; handlerTimeoutMs?: number } = {}) {
  const context = contexts()
  const core = coreFixture()
  core.getAppConfiguration.mockImplementation(async (id) => {
    if (id === slowAppId) await options.appGate
    return { ...app, app: { ...app.app, id } }
  })
  const factory = createGitHubFactory({ core })
  const submitInbound = vi.fn(unexpectedTestCall)
  const registry = new AppRuntimeRegistry({
    client: {
      ...core,
      resolveInteraction: unexpectedTestCall,
      resolveRuntimeInteraction: unexpectedTestCall,
      submitInbound,
      submitRuntimeInbound: unexpectedTestCall,
    },
    factories: new Map([[providerFactoryKey(factory.connectorKey, factory.provider), factory]]),
    logger: context.factory.logger,
    maxApps: 2,
    maxConcurrentLoads: 2,
    maxInstallations: 2,
    notFoundCacheMs: 1_000,
    providerLifecycleTimeoutMs: 1_000,
    reserveWorkBytes: context.budget.reserve,
    refreshAfterMs: 60_000,
  })
  const handles = new Map<string, RuntimeHandle>()
  const acquire = registry.acquire.bind(registry)
  vi.spyOn(registry, 'acquire').mockImplementation(async (id) => {
    const handle = await acquire(id)
    vi.spyOn(handle, 'handleWebhook')
    vi.spyOn(handle, 'release')
    handles.set(id, handle)
    return handle
  })
  const server = new GatewayServer({
    bodyLimitBytes: 24 * 1024 * 1024,
    handlerTimeoutMs: options.handlerTimeoutMs ?? 30_000,
    httpShutdownTimeoutMs: 100,
    logger: context.factory.logger,
    maxConcurrentRequests: 4,
    port: 0,
    publicUrl: 'https://gateway.example.test',
    registry,
    workBudget: context.budget,
  })
  const port = await server.listen()
  return { ...context, core, factory, handles, port, registry, server, submitInbound }
}

describe('provider webhook deadline from HTTP entry', () => {
  it('applies the real GitHub factory body limit before adapter invocation', async () => {
    const f = await fixture()
    try {
      expect(f.factory.webhookBodyLimitBytes).toBe(2 * 1024 * 1024)
      expect(
        await incompleteRequest(f.port, `/hooks/${app.app.id}/github/events`, {
          'content-length': String(2 * 1024 * 1024 + 1),
        }),
      ).toMatchObject({ status: 413 })
      expect(f.handles.get(app.app.id)?.handleWebhook).not.toHaveBeenCalled()
      expect(f.handles.get(app.app.id)?.release).toHaveBeenCalledOnce()
      expect(f.submitInbound).not.toHaveBeenCalled()
      expect(f.budget.usedBytes).toBe(0)
    } finally {
      await f.server.close()
      await f.registry.close()
    }
  })

  it('applies the real GitHub 9s cap to slow acquisition and incomplete bodies, releasing late handles', async () => {
    const gate = deferred()
    const f = await fixture({ appGate: gate.promise })
    try {
      expect(f.factory.webhookTimeoutMs).toBe(9_000)
      expect(f.registry.webhookTimeoutMs('github')).toBe(9_000)
      const started = Date.now()
      const acquisition = fetch(`http://127.0.0.1:${f.port}/hooks/${slowAppId}/github`, {
        signal: AbortSignal.timeout(11_000),
      })
      const body = incompleteRequest(
        f.port,
        `/hooks/${app.app.id}/github`,
        { 'content-type': 'application/json', 'transfer-encoding': 'chunked' },
        11_000,
      )
      const [acquisitionResponse, bodyResponse] = await Promise.all([acquisition, body])
      expect(acquisitionResponse.status).toBe(500)
      expect(await acquisitionResponse.text()).toBe('internal server error')
      expect(bodyResponse).toMatchObject({ status: 500 })
      expect(Date.now() - started).toBeGreaterThanOrEqual(8_900)
      expect(Date.now() - started).toBeLessThan(11_000)
      expect(f.handles.has(slowAppId)).toBe(false)
      const bodyHandle = f.handles.get(app.app.id)
      expect(bodyHandle).toBeDefined()
      expect(bodyHandle?.handleWebhook).not.toHaveBeenCalled()
      expect(bodyHandle?.release).toHaveBeenCalledOnce()

      // A shared app load remains usable; this expired request releases its
      // eventual reference without invoking the adapter or accepting input.
      gate.resolve()
      await vi.waitFor(() => {
        expect(f.handles.get(slowAppId)?.release).toHaveBeenCalledOnce()
      })
      expect(f.handles.get(slowAppId)?.handleWebhook).not.toHaveBeenCalled()
      expect(f.submitInbound).not.toHaveBeenCalled()
      expect(f.budget.usedBytes).toBe(0)
      const metrics = await (await fetch(`http://127.0.0.1:${f.port}/metrics`)).text()
      expect(metrics).toContain('omnara_channel_gateway_active_webhook_requests 0')
    } finally {
      gate.resolve()
      await f.server.close()
      await f.registry.close()
    }
  }, 13_000)

  it('honors a shorter operator budget through the GitHub adapter into actual core HTTP', async () => {
    let requested = false
    let closed = false
    const url = await localServer((_request, response) => {
      requested = true
      response.on('close', () => {
        closed = true
      })
      // The socket stays open until the gateway's entry signal cancels fetch.
    })
    const realCore = new CoreClient({ baseUrl: url, token: 'local-test', requestTimeoutMs: 30_000 })
    const f = await fixture({ handlerTimeoutMs: 250 })
    f.core.resolveInstallationConfiguration.mockImplementation((...args) =>
      realCore.resolveInstallationConfiguration(...args),
    )
    try {
      const request = signedRequest()
      const response = await fetch(`http://127.0.0.1:${f.port}/hooks/${app.app.id}/github`, {
        method: 'POST',
        headers: request.headers,
        body: await request.text(),
        signal: AbortSignal.timeout(2_000),
      })
      expect(response.status).toBe(500)
      await response.text()
      expect(requested).toBe(true)
      await vi.waitFor(() => {
        expect(closed).toBe(true)
      })
      expect(f.handles.get(app.app.id)?.handleWebhook).toHaveBeenCalledOnce()
      expect(f.handles.get(app.app.id)?.release).toHaveBeenCalledOnce()
      expect(f.submitInbound).not.toHaveBeenCalled()
      expect(f.budget.usedBytes).toBe(0)
    } finally {
      await f.server.close()
      await f.registry.close()
    }
  })
})
