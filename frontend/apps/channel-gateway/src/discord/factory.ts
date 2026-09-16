import type {
  ProviderFactory,
  ProviderFactoryContext,
  ProviderRuntime,
  ReceiptBehavior,
} from '../types'
import { DiscordAPIError } from './protocol'
import { type DiscordRuntimeOptions, runDiscordUnit } from './runtime'

export interface DiscordFactoryOptions extends DiscordRuntimeOptions {
  /** Required before registration: accepted receipts must have real behavior. */
  processReceipt: ReceiptBehavior
}

export function createDiscordFactory(options: DiscordFactoryOptions): ProviderFactory {
  return {
    connectorKey: 'omnara',
    provider: 'discord',
    create: (context: ProviderFactoryContext): Promise<ProviderRuntime> => {
      const lifetime = new AbortController()
      const signal = AbortSignal.any([context.signal, lifetime.signal])
      const units = new Map<string, Promise<void>>()
      return Promise.resolve<ProviderRuntime>({
        processReceipt: options.processReceipt,
        handleWebhook: () =>
          Promise.resolve(new Response('Discord uses the Gateway', { status: 404 })),
        runUnit: (unit, work) => {
          if (signal.aborted || units.has(unit.id))
            return Promise.reject(new DiscordAPIError('runtime_already_active_or_closed'))
          const running = runDiscordUnit({ ...context, signal }, unit, work, options)
          units.set(unit.id, running)
          void running
            .finally(() => {
              units.delete(unit.id)
            })
            .catch(() => undefined)
          return running
        },
        close: async () => {
          lifetime.abort()
          await Promise.allSettled(units.values())
        },
      })
    },
  }
}
