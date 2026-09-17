import { ApiError, type ChannelConnectorInstallationConfiguration } from '@omnara/sdk'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CoreClient } from '../core/client'
import { InstallationConfigurationCache, LoadLimiter } from './configuration-cache'

describe('channel configuration load limiter', () => {
  it('transfers a released permit without exceeding the concurrency limit', async () => {
    const limiter = new LoadLimiter(1)
    const firstGate = deferred()
    const secondGate = deferred()
    let active = 0
    let maximum = 0
    const run = (gate: Promise<void>) =>
      limiter.run(async () => {
        active += 1
        maximum = Math.max(maximum, active)
        await gate
        active -= 1
      })

    const first = run(firstGate.promise)
    const second = run(secondGate.promise)
    await vi.waitFor(() => {
      expect(active).toBe(1)
    })
    expect(maximum).toBe(1)

    firstGate.resolve()
    await first
    await vi.waitFor(() => {
      expect(active).toBe(1)
    })
    expect(maximum).toBe(1)

    secondGate.resolve()
    await second
    expect(active).toBe(0)
  })

  it('rejects queued waiters when the registry shuts down', async () => {
    const limiter = new LoadLimiter(1)
    const gate = deferred()
    const first = limiter.run(() => gate.promise)
    const queued = limiter.run(() => Promise.resolve())
    await Promise.resolve()

    limiter.close()
    await expect(queued).rejects.toThrow('load limiter is closed')
    gate.resolve()
    await first
  })
})

describe('channel installation configuration cache', () => {
  afterEach(() => vi.restoreAllMocks())

  it.each(['app', 'tenant', 'account'] as const)(
    'rejects a mismatched %s without caching it',
    async (field) => {
      const wrong = testInstallationConfiguration('install-1', 'tenant-1', 'account-1')
      if (field === 'app') wrong.integration_app_id = 'other-app'
      else if (field === 'tenant') wrong.install.provider_tenant_id = 'other-tenant'
      else wrong.install.provider_account_ref = 'other-account'
      const resolveInstallationConfiguration = vi.fn().mockResolvedValue(wrong)
      const cache = installationCache({ resolveInstallationConfiguration })

      for (let attempt = 0; attempt < 2; attempt++) {
        await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).rejects.toThrow(
          'mismatched installation configuration',
        )
      }
      expect(resolveInstallationConfiguration).toHaveBeenCalledTimes(2)
    },
  )

  it('caches an omitted tenant only for a tenantless lookup', async () => {
    const configuration: ChannelConnectorInstallationConfiguration = testInstallationConfiguration(
      'install-1',
      '',
      'account-1',
    )
    delete configuration.install.provider_tenant_id
    const resolveInstallationConfiguration = vi.fn().mockResolvedValue(configuration)
    const cache = installationCache({ resolveInstallationConfiguration })

    await expect(cache.resolve('app-1', '', 'account-1', 1)).resolves.toBe(configuration)
    await expect(cache.resolve('app-1', '', 'account-1', 1)).resolves.toBe(configuration)
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
    await expect(cache.resolve('app-1', 'other-tenant', 'account-1', 1)).rejects.toThrow(
      'mismatched installation configuration',
    )
  })

  it('refreshes credentials and replaces a reinstalled account after expiry', async () => {
    const now = vi.spyOn(Date, 'now').mockReturnValue(0)
    const resolveInstallationConfiguration = vi
      .fn()
      .mockResolvedValueOnce(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 1))
      .mockResolvedValueOnce(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 2))
      .mockResolvedValueOnce(testInstallationConfiguration('install-2', 'tenant-1', 'account-1', 1))
    const cache = installationCache({ resolveInstallationConfiguration })
    await cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    now.mockReturnValue(1_000)
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { id: 'install-1', configuration_revision: 2 },
    })
    now.mockReturnValue(2_000)
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { id: 'install-2', configuration_revision: 1 },
    })
    await cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    expect(resolveInstallationConfiguration).toHaveBeenCalledTimes(3)
  })

  it.each([1, 2])(
    'checks each coalesced caller when core returns app revision %i',
    async (revision) => {
      const pending = deferredValue<ChannelConnectorInstallationConfiguration>()
      const resolveInstallationConfiguration = vi.fn().mockReturnValueOnce(pending.promise)
      const cache = installationCache({ resolveInstallationConfiguration })
      const oldCall = cache.resolve('app-1', 'tenant-1', 'account-1', 1)
      const newCall = cache.resolve('app-1', 'tenant-1', 'account-1', 2)
      const matching = revision === 1 ? oldCall : newCall
      const stale = expect(revision === 1 ? newCall : oldCall).rejects.toThrow(
        'configuration revision changed',
      )
      const configuration = {
        ...testInstallationConfiguration('install-1', 'tenant-1', 'account-1'),
        app_configuration_revision: revision,
      }
      pending.resolve(configuration)
      await expect(matching).resolves.toBe(configuration)
      await stale
      await expect(cache.resolve('app-1', 'tenant-1', 'account-1', revision)).resolves.toBe(
        configuration,
      )
      expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
    },
  )

  it('refreshes on an app revision change and keeps the current configuration for stale callers', async () => {
    const resolveInstallationConfiguration = vi
      .fn()
      .mockResolvedValue(testInstallationConfiguration('install-1', 'tenant-1', 'account-1'))
    const cache = installationCache({ resolveInstallationConfiguration })
    await cache.resolve('app-1', 'tenant-1', 'account-1', 1)

    const refreshed = {
      ...testInstallationConfiguration('install-1', 'tenant-1', 'account-1'),
      app_configuration_revision: 2,
    }
    resolveInstallationConfiguration.mockResolvedValue(refreshed)
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 2)).resolves.toBe(refreshed)
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).rejects.toThrow(
      'configuration revision changed',
    )
    // A stale caller cannot replace the cached current app configuration.
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 2)).resolves.toBe(refreshed)
    expect(resolveInstallationConfiguration).toHaveBeenCalledTimes(3)
  })

  it('evicts the least recently used account and bounds outstanding loads', async () => {
    const resolveInstallationConfiguration = vi.fn(
      (_app: string, tenant: string, account: string) =>
        Promise.resolve(testInstallationConfiguration('install-' + account, tenant, account)),
    )
    const cache = installationCache({ resolveInstallationConfiguration }, 2)
    await cache.resolve('app-1', 'tenant-1', 'a', 1)
    await cache.resolve('app-1', 'tenant-1', 'b', 1)
    await cache.resolve('app-1', 'tenant-1', 'a', 1)
    await cache.resolve('app-1', 'tenant-1', 'c', 1)
    await cache.resolve('app-1', 'tenant-1', 'a', 1)
    expect(resolveInstallationConfiguration).toHaveBeenCalledTimes(3)
    await cache.resolve('app-1', 'tenant-1', 'b', 1)
    expect(resolveInstallationConfiguration).toHaveBeenCalledTimes(4)

    const pending = deferredValue<ChannelConnectorInstallationConfiguration>()
    const bounded = installationCache(
      { resolveInstallationConfiguration: () => pending.promise },
      1,
    )
    const first = bounded.resolve('app-1', 'tenant-1', 'a', 1)
    await expect(bounded.resolve('app-1', 'tenant-1', 'b', 1)).rejects.toThrow('at capacity')
    pending.resolve(testInstallationConfiguration('install-a', 'tenant-1', 'a'))
    await first
  })

  it('expires missing-account results and never caches transient failures', async () => {
    const now = vi.spyOn(Date, 'now').mockReturnValue(0)
    const resolveInstallationConfiguration = vi
      .fn()
      .mockRejectedValueOnce(new ApiError(404, 'not found'))
      .mockRejectedValueOnce(new ApiError(503, 'unavailable'))
      .mockResolvedValue(testInstallationConfiguration('install-1', 'tenant-1', 'account-1'))
    const cache = installationCache({ resolveInstallationConfiguration })
    for (let attempt = 0; attempt < 2; attempt++) {
      await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).rejects.toThrow('not found')
    }
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
    now.mockReturnValue(100)
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).rejects.toThrow('unavailable')
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { id: 'install-1' },
    })
    expect(resolveInstallationConfiguration).toHaveBeenCalledTimes(3)
  })

  it('rejects new work during shutdown and drains outstanding loads', async () => {
    const pending = deferredValue<ChannelConnectorInstallationConfiguration>()
    const cache = installationCache({ resolveInstallationConfiguration: () => pending.promise })
    const first = cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    const closing = cache.close()
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).rejects.toThrow(
      'cache is closed',
    )
    pending.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1'))
    await first
    await closing
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).rejects.toThrow(
      'cache is closed',
    )
  })
})

function deferred() {
  let resolve!: (value: undefined) => void
  const promise = new Promise<undefined>((settle) => {
    resolve = settle
  })
  return {
    promise,
    resolve: () => {
      resolve(undefined)
    },
  }
}

function deferredValue<T>() {
  let reject!: (error: Error) => void
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle, fail) => {
    reject = fail
    resolve = settle
  })
  return { promise, reject, resolve }
}

function installationCache(
  client: Pick<CoreClient, 'resolveInstallationConfiguration'>,
  maxEntries = 10,
): InstallationConfigurationCache {
  return new InstallationConfigurationCache({
    client,
    limiter: new LoadLimiter(2),
    maxEntries,
    notFoundCacheMs: 100,
    refreshAfterMs: 1_000,
  })
}

function testInstallationConfiguration(
  installId: string,
  tenantId: string,
  accountRef: string,
  revision = 1,
) {
  return {
    app_configuration_revision: 1,
    install: {
      project_id: 'proj_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      configuration_revision: revision,
      id: installId,
      provider_account_ref: accountRef,
      display_name: 'Test',
      provider_identity: {},
      metadata: {},
      provider_tenant_id: tenantId,
      updated_at: new Date().toISOString(),
    },
    integration_app_id: 'app-1',
  }
}
