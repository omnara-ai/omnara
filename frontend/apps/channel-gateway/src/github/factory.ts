import { schemas } from '@omnara/sdk'
import { z } from 'zod'

import type { CoreClient } from '../core/client'
import type { ProviderFactory, ProviderRuntime, ProviderWorkReservation } from '../types'
import { type GitHubBehaviorCore, processGitHubEvent } from './behavior'
import type { GitHubAuthentication } from './client'
import type { GitHubConfiguration } from './configuration'
import { githubWebhookBytes } from './events'
import { receiveGitHubWebhook } from './ingress'
import { type GitHubProviderControl, processGitHubControlReceipt } from './lifecycle'
import { GitHubAPIError } from './protocol'

export interface GitHubFactoryOptions {
  core: GitHubBehaviorCore &
    GitHubProviderControl &
    Pick<CoreClient, 'resolveInstallationConfiguration' | 'submitControlEvent'>
  /** Fixed operator/test endpoint only. Never taken from a webhook or route. */
  apiUrl?: string
}

export function createGitHubFactory(options: GitHubFactoryOptions): ProviderFactory {
  return {
    connectorKey: 'omnara',
    provider: 'github',
    webhookTimeoutMs: 9_000,
    webhookBodyLimitBytes: githubWebhookBytes,
    create: (context): Promise<ProviderRuntime> => {
      const configuration = context.configuration
      if (
        !schemas.zChannelConnectorAppConfiguration.safeParse(configuration).success ||
        !Number.isSafeInteger(configuration.app.configuration_revision)
      )
        throw new GitHubAPIError('invalid_configuration')
      if (
        configuration.app.provider !== 'github' ||
        configuration.app.connector_key !== 'omnara' ||
        configuration.credential?.kind !== 'integration_credentials'
      )
        throw new GitHubAPIError('invalid_configuration')
      const credential = z
        .object({
          private_key: z
            .string()
            .min(1)
            .max(32 * 1024),
          webhook_secret: z.string().min(1).max(4096),
        })
        .parse(configuration.credential.payload)
      const lifetime = new AbortController()
      const signal = AbortSignal.any([context.signal, lifetime.signal])
      const active = new Set<Promise<unknown>>()
      // Runtime-scoped, bounded auth only. Fresh core revisions select each slot;
      // repo addresses/availability and cancellation never survive a receipt.
      const authentication = new Map<string, GitHubAuthentication>()
      let authWork: ProviderWorkReservation | undefined
      function authenticationFor(
        config: GitHubConfiguration,
        appRevision: number,
        installRevision: number,
      ) {
        const key = `${config.appID}:${appRevision}:${config.integrationInstallID}:${installRevision}`
        let entry = authentication.get(key)
        if (!entry) {
          entry = {}
          // Token <=4KiB and node ID <=512 characters, with JS/storage overhead.
          authWork ??= context.reserveWorkBytes(0)
          authWork.resize(Math.min(authentication.size + 1, 64) * 16 * 1024)
          if (authentication.size === 64) {
            const oldest = authentication.keys().next().value
            if (oldest !== undefined) authentication.delete(oldest)
          }
        }
        authentication.delete(key)
        authentication.set(key, entry)
        return entry
      }
      async function track<T>(start: () => Promise<T>): Promise<T> {
        signal.throwIfAborted()
        const task = start()
        active.add(task)
        try {
          return await task
        } finally {
          active.delete(task)
        }
      }
      return Promise.resolve<ProviderRuntime>({
        handleWebhook: (request, work) =>
          track(() =>
            receiveGitHubWebhook(request, { ...context, signal }, work, {
              webhookSecret: credential.webhook_secret,
              resolveInstallation: (tenant, repository, requestSignal) =>
                options.core.resolveInstallationConfiguration(
                  configuration.app.id,
                  tenant,
                  repository,
                  requestSignal,
                ),
              saveControl: async (event, requestSignal) => {
                await options.core.submitControlEvent(
                  configuration.app.id,
                  {
                    event_id: event.delivery_id,
                    provider_tenant_id: event.installationID,
                    payload: event,
                  },
                  requestSignal,
                )
              },
            }),
          ),
        processReceipt: (receipt, work) =>
          track(() =>
            processGitHubEvent(
              receipt,
              {
                ...work,
                signal: AbortSignal.any([signal, work.signal]),
              },
              {
                core: options.core,
                apiUrl: options.apiUrl,
                reserveWorkBytes: context.reserveWorkBytes,
                authenticationFor,
              },
            ),
          ),
        processControlReceipt: (receipt, work) =>
          track(() =>
            processGitHubControlReceipt(
              receipt,
              {
                ...work,
                signal: AbortSignal.any([signal, work.signal]),
              },
              { core: options.core, apiUrl: options.apiUrl },
            ),
          ),
        close: async () => {
          lifetime.abort()
          await Promise.allSettled(active)
          authentication.clear()
          authWork?.release()
        },
      })
    },
  }
}
