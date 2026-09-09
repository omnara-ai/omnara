import type { GatewayLogFields, GatewayLogger } from './types'

export class JsonLogger implements GatewayLogger {
  debug(message: string, fields: GatewayLogFields = {}): void {
    this.write('debug', message, fields)
  }

  error(message: string, fields: GatewayLogFields = {}): void {
    this.write('error', message, fields)
  }

  info(message: string, fields: GatewayLogFields = {}): void {
    this.write('info', message, fields)
  }

  warn(message: string, fields: GatewayLogFields = {}): void {
    this.write('warn', message, fields)
  }

  private write(level: string, message: string, fields: GatewayLogFields): void {
    const line = JSON.stringify({ ...fields, level, message, timestamp: new Date().toISOString() })
    if (level === 'error') process.stderr.write(`${line}\n`)
    else process.stdout.write(`${line}\n`)
  }
}
