import type { Logger } from 'chat'

import { boundedDatabaseText } from './diagnostics'
import type { GatewayLogger } from './types'

/**
 * Keeps Chat SDK diagnostics in the gateway's structured log stream. Debug
 * arguments are deliberately discarded because upstream debug events may
 * contain message text or provider payloads.
 */
export function createChatSdkLogger(logger: GatewayLogger, integrationAppId: string): Logger {
  return new ChatSdkLogger(logger, integrationAppId)
}

class ChatSdkLogger implements Logger {
  constructor(
    private readonly logger: GatewayLogger,
    private readonly integrationAppId: string,
    private readonly scope?: string,
  ) {}

  child(prefix: string): Logger {
    return new ChatSdkLogger(
      this.logger,
      this.integrationAppId,
      this.scope ? `${this.scope}:${prefix}` : prefix,
    )
  }

  debug(): void {
    return
  }

  error(message: string, ...args: unknown[]): void {
    this.write('error', message, args)
  }

  info(message: string): void {
    this.write('info', message)
  }

  warn(message: string, ...args: unknown[]): void {
    this.write('warn', message, args)
  }

  private write(level: 'error' | 'info' | 'warn', message: string, args: unknown[] = []): void {
    const fields: Record<string, unknown> = {
      integration_app_id: this.integrationAppId,
      source: 'chat_sdk',
    }
    if (this.scope) fields.chat_sdk_scope = this.scope
    const error = errorFromArguments(args)
    if (error) fields.error = error
    this.logger[level](message, fields)
  }
}

function errorFromArguments(args: unknown[]): string | undefined {
  for (const argument of args) {
    if (argument instanceof Error) return errorMessage(argument)
    if (isRecord(argument) && argument.error !== undefined) {
      return errorMessage(argument.error)
    }
  }
  return undefined
}

function errorMessage(error: unknown): string {
  if (error instanceof Error) return boundedDatabaseText(error.message)
  if (typeof error === 'string') return boundedDatabaseText(error)
  if (typeof error === 'number' || typeof error === 'boolean' || typeof error === 'bigint') {
    return `${error}`
  }
  return 'unknown Chat SDK failure'
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}
