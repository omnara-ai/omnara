import type { CoreClient } from './core-client'
import { processDiscordEvent } from './discord/behavior'
import { createDiscordFactory } from './discord/factory'
import { createGitHubFactory } from './github/factory'
import type { RedisStateClient } from './redis-client'
import type { ProviderFactory, ProviderFactoryContext, ReceiptBehavior } from './types'

export interface BuiltInProviderOptions {
  core: CoreClient
  redis: RedisStateClient
  reserveWorkBytes: ProviderFactoryContext['reserveWorkBytes']
  slackReceipt: ReceiptBehavior
  stopTimeoutMs: number
  onFatalRuntimeFailure: (error: Error) => never
}

export function builtInProviderFactories(options: BuiltInProviderOptions): ProviderFactory[] {
  return [
    {
      connectorKey: 'omnara',
      provider: 'slack',
      // Slack's existing public API endpoints verify and durably capture its
      // callbacks. This registration supplies their shared receipt consumer.
      create: () =>
        Promise.resolve({
          close: () => Promise.resolve(),
          handleWebhook: () => Promise.resolve(new Response('Not found', { status: 404 })),
          processReceipt: options.slackReceipt,
        }),
    },
    createDiscordFactory({
      redis: options.redis,
      stopTimeoutMs: options.stopTimeoutMs,
      onFatalRuntimeFailure: options.onFatalRuntimeFailure,
      processReceipt: (receipt, context) =>
        processDiscordEvent(receipt, {
          ...context,
          core: options.core,
          reserveWorkBytes: options.reserveWorkBytes,
        }),
    }),
    createGitHubFactory({ core: options.core }),
  ]
}
