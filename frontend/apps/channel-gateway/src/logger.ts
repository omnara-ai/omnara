import type { GatewayLogger } from './types'

export class JsonLogger implements GatewayLogger {
  debug(message: string, fields: Record<string, unknown> = {}): void {
    this.write('debug', message, fields)
  }

  error(message: string, fields: Record<string, unknown> = {}): void {
    this.write('error', message, fields)
  }

  info(message: string, fields: Record<string, unknown> = {}): void {
    this.write('info', message, fields)
  }

  warn(message: string, fields: Record<string, unknown> = {}): void {
    this.write('warn', message, fields)
  }

  private write(level: string, message: string, fields: Record<string, unknown>): void {
    const line = JSON.stringify({ ...fields, level, message, timestamp: new Date().toISOString() })
    if (level === 'error') process.stderr.write(`${line}\n`)
    else process.stdout.write(`${line}\n`)
  }
}
