import { ApiError } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { InstallationConfigurationCache, LoadLimiter } from './configuration-cache'
import type { CoreClient } from './core-client'

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

describe('channel installation configuration cache identity', () => {
  it('rejects a get-by-ID response for a different app or installation without caching it', async () => {
    const getInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('different-install', 'tenant-1', 'account-1')),
    )
    const cache = installationCache({ getInstallationConfiguration })

    await expect(cache.getByID('app-1', 'install-1', 1, 1)).rejects.toThrow(
      'mismatched installation configuration',
    )
    await expect(cache.getByID('app-1', 'install-1', 1, 1)).rejects.toThrow(
      'mismatched installation configuration',
    )

    expect(getInstallationConfiguration).toHaveBeenCalledTimes(2)
  })

  it('rejects an external lookup response for a different tenant or account', async () => {
    const resolveInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'other-tenant', 'account-1')),
    )
    const cache = installationCache({ resolveInstallationConfiguration })

    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).rejects.toThrow(
      'mismatched installation configuration',
    )

    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
  })

  it('keeps external aliases coherent after a by-ID refresh', async () => {
    const resolveInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 1)),
    )
    const getInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 2)),
    )
    const cache = installationCache({
      getInstallationConfiguration,
      resolveInstallationConfiguration,
    })

    const first = await cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    expect(first.install.configuration_revision).toBe(1)
    const refreshed = await cache.getByID('app-1', 'install-1', 1, 2)
    expect(refreshed.install.configuration_revision).toBe(2)
    const aliased = await cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    expect(aliased.install.configuration_revision).toBe(2)
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
    expect(getInstallationConfiguration).toHaveBeenCalledOnce()
  })

  it('does not let a delayed external lookup replace a newer canonical revision', async () => {
    const external = deferredValue<ReturnType<typeof testInstallationConfiguration>>()
    const resolveInstallationConfiguration = vi.fn(() => external.promise)
    const getInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 2)),
    )
    const cache = installationCache(
      { getInstallationConfiguration, resolveInstallationConfiguration },
      2,
    )

    const delayed = cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    await vi.waitFor(() => {
      expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
    })
    const current = await cache.getByID('app-1', 'install-1', 1, 2)
    external.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 1))

    expect(current.install.configuration_revision).toBe(2)
    await expect(delayed).resolves.toMatchObject({ install: { configuration_revision: 2 } })
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { configuration_revision: 2 },
    })
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
  })

  it('does not let a delayed exact lookup reclaim a reinstalled external alias', async () => {
    const oldInstall = deferredValue<ReturnType<typeof testInstallationConfiguration>>()
    const getInstallationConfiguration = vi.fn(() => oldInstall.promise)
    const resolveInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-2', 'tenant-1', 'account-1', 1)),
    )
    const cache = installationCache(
      { getInstallationConfiguration, resolveInstallationConfiguration },
      2,
    )

    const delayed = cache.getByID('app-1', 'install-1', 1, 7)
    await vi.waitFor(() => {
      expect(getInstallationConfiguration).toHaveBeenCalledOnce()
    })
    const reinstalled = await cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    oldInstall.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 7))

    expect(reinstalled.install.id).toBe('install-2')
    await expect(delayed).resolves.toMatchObject({ install: { id: 'install-1' } })
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { id: 'install-2' },
    })
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
  })

  it('never lets a later exact lookup reclaim another installation external alias', async () => {
    const getInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 7)),
    )
    const resolveInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-2', 'tenant-1', 'account-1', 1)),
    )
    const cache = installationCache({
      getInstallationConfiguration,
      resolveInstallationConfiguration,
    })

    await cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    await expect(cache.getByID('app-1', 'install-1', 1, 7)).resolves.toMatchObject({
      install: { id: 'install-1' },
    })

    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { id: 'install-2' },
    })
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
  })

  it('lets the external resolver replace an alias populated by a later exact lookup', async () => {
    const replacement = deferredValue<ReturnType<typeof testInstallationConfiguration>>()
    const resolveInstallationConfiguration = vi.fn(() => replacement.promise)
    const getInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 7)),
    )
    const cache = installationCache(
      { getInstallationConfiguration, resolveInstallationConfiguration },
      2,
    )

    const resolving = cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    await vi.waitFor(() => {
      expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
    })
    await cache.getByID('app-1', 'install-1', 1, 7)
    replacement.resolve(testInstallationConfiguration('install-2', 'tenant-1', 'account-1', 1))

    await expect(resolving).resolves.toMatchObject({ install: { id: 'install-2' } })
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { id: 'install-2' },
    })
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
  })

  it('does not let a delayed external 404 hide a newer by-ID result', async () => {
    const external = deferredValue<ReturnType<typeof testInstallationConfiguration>>()
    const resolveInstallationConfiguration = vi.fn(() => external.promise)
    const getInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 2)),
    )
    const cache = installationCache(
      { getInstallationConfiguration, resolveInstallationConfiguration },
      2,
    )

    const delayed = cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    const rejection = expect(delayed).rejects.toThrow('not found')
    await vi.waitFor(() => {
      expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
    })
    await cache.getByID('app-1', 'install-1', 1, 2)
    external.reject(new ApiError(404, 'not found'))

    await rejection
    await expect(cache.resolve('app-1', 'tenant-1', 'account-1', 1)).resolves.toMatchObject({
      install: { configuration_revision: 2 },
    })
    expect(resolveInstallationConfiguration).toHaveBeenCalledOnce()
  })

  it('does not let a delayed by-ID 404 hide a newer external result', async () => {
    const byID = deferredValue<ReturnType<typeof testInstallationConfiguration>>()
    const getInstallationConfiguration = vi.fn(() => byID.promise)
    const resolveInstallationConfiguration = vi.fn(() =>
      Promise.resolve(testInstallationConfiguration('install-1', 'tenant-1', 'account-1', 2)),
    )
    const cache = installationCache(
      { getInstallationConfiguration, resolveInstallationConfiguration },
      2,
    )

    const delayed = cache.getByID('app-1', 'install-1', 1, 1)
    const rejection = expect(delayed).rejects.toThrow('not found')
    await vi.waitFor(() => {
      expect(getInstallationConfiguration).toHaveBeenCalledOnce()
    })
    await cache.resolve('app-1', 'tenant-1', 'account-1', 1)
    byID.reject(new ApiError(404, 'not found'))

    await rejection
    await expect(cache.getByID('app-1', 'install-1', 1, 2)).resolves.toMatchObject({
      install: { configuration_revision: 2 },
    })
    expect(getInstallationConfiguration).toHaveBeenCalledOnce()
  })
})

function deferred(): { promise: Promise<undefined>; resolve: () => void } {
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

function deferredValue<T>(): {
  promise: Promise<T>
  reject: (error: unknown) => void
  resolve: (value: T) => void
} {
  let reject!: (error: unknown) => void
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle, fail) => {
    reject = fail
    resolve = settle
  })
  return { promise, reject, resolve }
}

function installationCache(
  client: Partial<CoreClient>,
  concurrentLoads = 1,
): InstallationConfigurationCache {
  return new InstallationConfigurationCache({
    client: client as CoreClient,
    limiter: new LoadLimiter(concurrentLoads),
    maxEntries: 10,
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
      configuration_revision: revision,
      id: installId,
      provider_account_ref: accountRef,
      provider_agent_display_name: 'Test',
      provider_config: {},
      provider_identity: {},
      provider_metadata: {},
      provider_tenant_id: tenantId,
      updated_at: new Date().toISOString(),
    },
    integration_app_id: 'app-1',
  }
}
