import { describe, expect, it, vi } from 'vitest'

import { testAppConfiguration, unexpectedTestCall } from '../gateway-test-fixtures'
import { AppRuntimeRegistry } from '../runtime/registry'
import { type ProviderFactory, providerFactoryKey } from '../types'
import { WorkByteBudget } from '../work-budget'
import { GatewayServer } from './server'
import {
  incompleteRequest,
  integrationAppId,
  providerRuntime,
  startServer,
  streamedRequest,
} from './server-test-support'

const webhookPath = `/hooks/${integrationAppId}/github/events`
const providerLimit = 2 * 1024 * 1024
const options = {
  provider: 'github',
  bodyLimitBytes: 24 * 1024 * 1024,
  webhookBodyLimitBytes: providerLimit,
  maxBufferedWorkBytes: 8 * 1024 * 1024,
}

describe('provider webhook body limit at HTTP entry', () => {
  it('uses the exact connector/provider factory without restricting its same-provider neighbors', async () => {
    const workBudget = new WorkByteBudget(options.maxBufferedWorkBytes)
    const handler = vi.fn(async (request: Request) => {
      expect((await request.text()).length).toBe(providerLimit + 1)
      return new Response('accepted')
    })
    const create = () => Promise.resolve(providerRuntime(handler))
    const factories: ProviderFactory[] = [
      { connectorKey: 'small', provider: 'github', webhookBodyLimitBytes: providerLimit, create },
      {
        connectorKey: 'larger',
        provider: 'github',
        webhookBodyLimitBytes: 3 * 1024 * 1024,
        create,
      },
      { connectorKey: 'uncapped', provider: 'github', create },
    ]
    const apps = new Map([
      [integrationAppId, 'small'],
      [`iapp_${'b'.repeat(26)}`, 'larger'],
      [`iapp_${'c'.repeat(26)}`, 'uncapped'],
    ])
    const logger = { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() }
    const registry = new AppRuntimeRegistry({
      client: {
        getAppConfiguration: (id) =>
          Promise.resolve(
            testAppConfiguration({
              id,
              provider: 'github',
              connector_key: apps.get(id),
            }),
          ),
        getInstallationConfiguration: unexpectedTestCall,
        resolveInstallationConfiguration: unexpectedTestCall,
        resolveInteraction: unexpectedTestCall,
        resolveRuntimeInteraction: unexpectedTestCall,
        submitInbound: unexpectedTestCall,
        submitRuntimeInbound: unexpectedTestCall,
      },
      factories: new Map(factories.map((f) => [providerFactoryKey(f.connectorKey, f.provider), f])),
      logger,
      maxApps: 3,
      maxConcurrentLoads: 2,
      maxInstallations: 2,
      notFoundCacheMs: 1_000,
      providerLifecycleTimeoutMs: 1_000,
      reserveWorkBytes: workBudget.reserve,
      refreshAfterMs: 60_000,
    })
    const server = new GatewayServer({
      bodyLimitBytes: options.bodyLimitBytes,
      handlerTimeoutMs: 1_000,
      httpShutdownTimeoutMs: 100,
      logger,
      maxConcurrentRequests: 4,
      port: 0,
      publicUrl: 'https://gateway.example.test',
      registry,
      workBudget,
    })
    const port = await server.listen()
    try {
      for (const [id, connector] of apps) {
        const response = await streamedRequest(port, `/hooks/${id}/github/events`, [
          'x'.repeat(providerLimit + 1),
        ])
        expect(response.status).toBe(connector === 'small' ? 413 : 200)
        expect(workBudget.usedBytes).toBe(0)
      }
      expect(handler).toHaveBeenCalledTimes(2)
    } finally {
      await server.close()
      await registry.close()
    }
  })

  it('rejects oversized declared bodies after acquisition without buffering or waiting for the upload', async () => {
    const handler = vi.fn(() => Promise.resolve(new Response('accepted')))
    const f = await startServer(providerRuntime(handler), options)
    try {
      expect(
        await incompleteRequest(f.port, webhookPath, {
          'content-length': String(providerLimit + 1),
        }),
      ).toMatchObject({ status: 413 })
      expect(f.registry.webhookBodyLimitBytes).toHaveBeenCalledWith('test_connector', 'github')
      expect(f.registry.acquire).toHaveBeenCalledOnce()
      expect(f.release).toHaveBeenCalledOnce()
      expect(handler).not.toHaveBeenCalled()
      expect(f.workBudget.usedBytes).toBe(0)
    } finally {
      await f.server.close()
    }
  })

  it('enforces the tighter limit on chunked bodies and releases work for the next request', async () => {
    const handler = vi.fn(async (request: Request) => new Response(await request.text()))
    const f = await startServer(providerRuntime(handler), options)
    try {
      const chunks = Array.from({ length: 64 }, () => 'x'.repeat(32 * 1024))
      const response = await streamedRequest(f.port, webhookPath, [...chunks, 'x'])
      expect(response).toEqual({ status: 413, body: 'request body too large' })
      expect(handler).not.toHaveBeenCalled()
      expect(f.release).toHaveBeenCalledOnce()
      expect(f.workBudget.usedBytes).toBe(0)
      expect(await streamedRequest(f.port, webhookPath, ['small input'])).toEqual({
        status: 200,
        body: 'small input',
      })
      expect(handler).toHaveBeenCalledOnce()
      expect(f.workBudget.usedBytes).toBe(0)
    } finally {
      await f.server.close()
    }
  })

  it('forwards an exact-limit raw body intact', async () => {
    const handler = vi.fn(async (request: Request) => {
      expect(await request.text()).toBe('x'.repeat(providerLimit))
      return new Response('accepted')
    })
    const f = await startServer(providerRuntime(handler), options)
    try {
      expect(await streamedRequest(f.port, webhookPath, ['x'.repeat(providerLimit)])).toEqual({
        status: 200,
        body: 'accepted',
      })
      expect(handler).toHaveBeenCalledOnce()
      expect(f.workBudget.usedBytes).toBe(0)
    } finally {
      await f.server.close()
    }
  })

  it('keeps a tighter operator limit', async () => {
    const handler = vi.fn(() => Promise.resolve(new Response('accepted')))
    const f = await startServer(providerRuntime(handler), { ...options, bodyLimitBytes: 1024 })
    try {
      expect(await streamedRequest(f.port, webhookPath, ['x'.repeat(1025)])).toEqual({
        status: 413,
        body: 'request body too large',
      })
      expect(handler).not.toHaveBeenCalled()
      expect(f.workBudget.usedBytes).toBe(0)
    } finally {
      await f.server.close()
    }
  })
})
