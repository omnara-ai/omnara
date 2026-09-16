import { createOmnaraClient } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { listInstallationControlScopes, setInstallationProviderState } from './provider-state'

const app = 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa'
const install = 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa'
const stateRequest = {
  provider_tenant_id: '42',
  provider_account_ref: '101',
  expected_app_configuration_revision: 1,
  expected_configuration_revision: 7,
  state: 'active' as const,
}
const page = {
  app_configuration_revision: 1,
  installations: [
    {
      id: install,
      project_id: 'proj_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      provider_tenant_id: '42',
      provider_account_ref: '101',
      state: 'disabled',
      configuration_revision: 7,
      provider_identity: { repository_node_id: 'R_saved' },
    },
  ],
  through_installation_id: install,
  next_after_installation_id: null,
}

function client(fetch: typeof globalThis.fetch) {
  return createOmnaraClient({
    baseUrl: 'http://core.test/api/v1',
    headers: { authorization: 'Bearer private-control-token' },
    fetch,
  })
}

describe('installation provider control', () => {
  it('accepts a full page at the verified GitHub identity limits', async () => {
    // Keep the private reader usable at the setup bounds in
    // internal/integration/github/identity.go, including the maximum page size.
    const alphabet = 'abcdefghijklmnopqrstuvwxyz234567'
    const installations = Array.from({ length: 100 }, (_, index) => ({
      ...page.installations[0],
      id: `iin_${'a'.repeat(24)}${alphabet[Math.floor(index / 32)]}${alphabet[index % 32]}`,
      provider_identity: {
        repository_node_id: 'R'.repeat(512),
        repository_owner: 'o'.repeat(100),
        repository_name: 'n'.repeat(100),
      },
    }))
    const result = { ...page, installations, through_installation_id: installations.at(-1)?.id }
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(result))
    expect(
      await listInstallationControlScopes(
        client(fetch),
        app,
        { provider_tenant_id: '42', limit: 100 },
        new AbortController().signal,
      ),
    ).toEqual(result)
  })

  it('uses the scoped generated routes and preserves disabled descriptors and revision fences', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(Response.json(page))
      .mockResolvedValueOnce(Response.json({ state: 'active', configuration_revision: 8 }))
    const core = client(fetch)
    const signal = new AbortController().signal
    expect(
      await listInstallationControlScopes(
        core,
        app,
        { provider_tenant_id: '42', limit: 1 },
        signal,
      ),
    ).toEqual(page)
    expect(await setInstallationProviderState(core, app, install, stateRequest, signal)).toEqual({
      state: 'active',
      configuration_revision: 8,
    })
    const [readCall, updateCall] = fetch.mock.calls
    if (!readCall || !updateCall) throw new Error('expected list and update requests')
    const read = new Request(readCall[0], readCall[1])
    const update = new Request(updateCall[0], updateCall[1])
    expect(read.url).toContain(
      `/apps/${app}/installation-control-scopes?provider_tenant_id=42&limit=1`,
    )
    expect(update.url).toBe(
      `http://core.test/api/v1/channel-connector/apps/${app}/installations/${install}/provider-state`,
    )
    expect(update.headers.get('authorization')).toBe('Bearer private-control-token')
    expect(update.redirect).toBe('error')
    expect(await update.json()).toEqual(stateRequest)
  })

  it.each([
    { ...page, installations: [{ ...page.installations[0], provider_tenant_id: 'other' }] },
    { ...page, installations: [page.installations[0], page.installations[0]] },
    { ...page, installations: [{ ...page.installations[0], state: 'future' }] },
    { ...page, installations: [], next_after_installation_id: install },
    { ...page, app_configuration_revision: 9007199254740992 },
  ])('rejects an uncorrelated control page: %j', async (result) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(result))
    await expect(
      listInstallationControlScopes(
        client(fetch),
        app,
        { provider_tenant_id: '42' },
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).toHaveBeenCalledOnce()
  })

  it.each([409, 503])('does not replay or expose a raw %i failure', async (status) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      Response.json(
        {
          error: 'private-provider-detail private-control-token',
        },
        { status },
      ),
    )
    const result = setInstallationProviderState(
      client(fetch),
      app,
      install,
      stateRequest,
      new AbortController().signal,
    )
    await expect(result).rejects.toMatchObject({ code: 'http_error', status })
    await expect(result).rejects.not.toThrow('private-provider-detail')
    await expect(result).rejects.not.toThrow('private-control-token')
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('rejects an exhausted revision before dispatch', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>()
    await expect(
      setInstallationProviderState(
        client(fetch),
        app,
        install,
        { ...stateRequest, expected_configuration_revision: Number.MAX_SAFE_INTEGER },
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'invalid_request' })
    expect(fetch).not.toHaveBeenCalled()
  })

  it.each([
    { state: 'disabled', configuration_revision: 8 },
    { state: 'active', configuration_revision: 7 },
    { state: 'active', configuration_revision: 9 },
  ])('rejects a state/revision acknowledgment mismatch: %j', async (result) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(result))
    await expect(
      setInstallationProviderState(
        client(fetch),
        app,
        install,
        stateRequest,
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('passes cancellation into actual I/O without retrying', async () => {
    const controller = new AbortController()
    let ioSignal: AbortSignal | undefined
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async (input, init) => {
      const requestSignal = new Request(input, init).signal
      ioSignal = requestSignal
      return new Promise<Response>((_resolve, reject) => {
        requestSignal.addEventListener(
          'abort',
          () => {
            reject(new DOMException('Aborted', 'AbortError'))
          },
          { once: true },
        )
        controller.abort()
      })
    })
    await expect(
      setInstallationProviderState(client(fetch), app, install, stateRequest, controller.signal),
    ).rejects.toMatchObject({ code: 'aborted' })
    expect(ioSignal?.aborted).toBe(true)
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('cancels an oversized response stream before SDK materialization', async () => {
    const cancel = vi.fn()
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new Uint8Array(1024 * 1024 + 1))
          },
          cancel,
        }),
      ),
    )
    await expect(
      listInstallationControlScopes(
        client(fetch),
        app,
        { provider_tenant_id: '42' },
        new AbortController().signal,
      ),
    ).rejects.toMatchObject({ code: 'response_too_large' })
    expect(cancel).toHaveBeenCalledOnce()
  })
})
