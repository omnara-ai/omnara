import { randomUUID } from 'node:crypto'

import { describe, expect, it, vi } from 'vitest'

import { CoreClient } from '../core-client'
import { ReceiptClientError } from '../receipt-http'
import { githubEvent } from './events'
import { createGitHubFactory } from './factory'
import {
  contexts,
  coreFixture,
  nativeFixture,
  receipt,
  signedRequest,
  webhook,
} from './inbound-test-support'
import { localServer, mutationInputs } from './test-support'

async function fixture() {
  const native = await nativeFixture()
  const context = contexts()
  const core = coreFixture()
  const runtime = await createGitHubFactory({ core, apiUrl: native.url }).create(context.factory)
  return { ...native, ...context, core, runtime }
}

describe('GitHub signed intake and receipt factory', () => {
  it('acknowledges only after durable core acceptance, then replays the saved projection through the PR workflow', async () => {
    const f = await fixture()
    let accept: (() => void) | undefined
    f.submitInbound.mockImplementation(
      () =>
        new Promise((resolve) => {
          accept = () => {
            resolve({ receipt_id: 'irec_aaaaaaaaaaaaaaaaaaaaaaaaaa', state: 'pending' })
          }
        }),
    )
    let acknowledged = false
    const intake = f.runtime.handleWebhook(signedRequest(), f.intake).then((response) => {
      acknowledged = true
      return response
    })
    await vi.waitFor(() => {
      expect(accept).toBeDefined()
    })
    expect(acknowledged).toBe(false)
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    accept?.()
    expect((await intake).status).toBe(202)
    const saved = githubEvent.parse(f.submitInbound.mock.calls[0]?.[0].payload)
    await f.runtime.processReceipt?.(receipt(saved), {
      signal: f.controller.signal,
      deadlineMs: Date.now() + 10_000,
    })
    expect(f.core.deliverWorkflow.mock.calls[0]?.[1].metadata).toMatchObject({
      raw_body_sha256: saved.raw_body_sha256,
      delivery_id: saved.delivery_id,
    })
    expect(mutationInputs(f.calls)).toEqual([])
    await f.runtime.close()
    expect(f.budget.usedBytes).toBe(0)
  })
  it('rejects invalid signatures before parsing or configuration I/O', async () => {
    const f = await fixture()
    const request = signedRequest('{invalid json')
    request.headers.set('x-hub-signature-256', `sha256=${'0'.repeat(64)}`)
    expect((await f.runtime.handleWebhook(request, f.intake)).status).toBe(401)
    expect(f.core.resolveInstallationConfiguration).not.toHaveBeenCalled()
    expect(f.core.submitControlEvent).not.toHaveBeenCalled()
    expect(f.submitInbound).not.toHaveBeenCalled()
    expect(f.budget.usedBytes).toBe(0)
    await f.runtime.close()
  })
  it.each(['too large', 'duplicate JSON', 'foreign repository', 'conflicting receipt'])(
    'fails %s before success without quiet truncation',
    async (mode) => {
      const f = await fixture()
      let body = JSON.stringify(webhook)
      let status = 400
      if (mode === 'too large') {
        body = ' '.repeat(2 * 1024 * 1024 + 1)
        status = 413
      }
      if (mode === 'duplicate JSON')
        body = body.replace('"action":"opened"', '"action":"opened","action":"closed"')
      if (mode === 'foreign repository') {
        f.core.resolveInstallationConfiguration.mockResolvedValue({
          ...(await f.core.getInstallationConfiguration('a', 'b')),
          install: {
            ...(await f.core.getInstallationConfiguration('a', 'b')).install,
            provider_account_ref: '789',
          },
        })
        status = 409
      }
      if (mode === 'conflicting receipt') {
        f.submitInbound.mockRejectedValue(new ReceiptClientError('http_error', 409))
        status = 409
      }
      expect((await f.runtime.handleWebhook(signedRequest(body), f.intake)).status).toBe(status)
      if (mode !== 'conflicting receipt') expect(f.submitInbound).not.toHaveBeenCalled()
      expect(f.budget.usedBytes).toBe(0)
      await f.runtime.close()
    },
  )
  it('accepts a near-2MiB raw body inside the default budget while saving only relevant communication', async () => {
    const f = await fixture()
    const raw = JSON.stringify({ ...webhook, irrelevant: 'x'.repeat(1_900_000) })
    expect((await f.runtime.handleWebhook(signedRequest(raw), f.intake)).status).toBe(202)
    expect(JSON.stringify(f.submitInbound.mock.calls[0]?.[0].payload).length).toBeLessThan(4096)
    expect(f.budget.usedBytes).toBe(0)
    await f.runtime.close()
  })
  it.each(['resolve', 'control'])(
    'propagates the caller deadline into actual core HTTP %s',
    async (path) => {
      let closed = false
      let requested = false
      const url = await localServer((_request, response) => {
        requested = true
        response.on('close', () => {
          closed = true
        })
        // Simulate an unresponsive core, not a detached mocked promise.
      })
      const core = coreFixture()
      const realCore = new CoreClient({
        baseUrl: url,
        token: 'local-test',
        requestTimeoutMs: 30_000,
      })
      core.resolveInstallationConfiguration.mockImplementation((...args) =>
        realCore.resolveInstallationConfiguration(...args),
      )
      core.submitControlEvent.mockImplementation((...args) => realCore.submitControlEvent(...args))
      const f = contexts()
      const runtime = await createGitHubFactory({ core }).create(f.factory)
      const start = Date.now()
      const request =
        path === 'resolve'
          ? signedRequest()
          : signedRequest(
              JSON.stringify({ action: 'deleted', installation: { id: 123, app_id: 42 } }),
              'installation',
            )
      // Production supplies the outer server's earlier request-entry deadline.
      const boundedRequest = new Request(request, { signal: AbortSignal.timeout(500) })
      const result = await runtime.handleWebhook(boundedRequest, f.intake)
      expect(result.status).toBe(503)
      expect(Date.now() - start).toBeLessThan(2_000)
      expect(requested).toBe(true)
      await vi.waitFor(() => {
        expect(closed).toBe(true)
      })
      expect(f.submitInbound).not.toHaveBeenCalled()
      expect(f.budget.usedBytes).toBe(0)
      await runtime.close()
    },
    5_000,
  )
  it('aborts an active scoped core request on close and never acknowledges it', async () => {
    const f = await fixture()
    let actualSignal: AbortSignal | undefined
    f.core.resolveInstallationConfiguration.mockImplementation(
      (_app, _tenant, _repo, signal) =>
        new Promise((_resolve, reject) => {
          actualSignal = signal
          signal?.addEventListener(
            'abort',
            () => {
              reject(new ReceiptClientError('aborted'))
            },
            {
              once: true,
            },
          )
        }),
    )
    const response = f.runtime.handleWebhook(signedRequest(), f.intake)
    await vi.waitFor(() => {
      expect(actualSignal).toBeDefined()
    })
    await f.runtime.close()
    expect(actualSignal?.aborted).toBe(true)
    expect((await response).status).toBe(503)
    expect(f.submitInbound).not.toHaveBeenCalled()
    expect(f.budget.usedBytes).toBe(0)
    await expect(
      f.runtime.handleWebhook(signedRequest('{}', 'ping', randomUUID()), f.intake),
    ).rejects.toThrow()
  })
})
