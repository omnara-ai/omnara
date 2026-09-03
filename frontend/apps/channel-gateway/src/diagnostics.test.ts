import { describe, expect, it } from 'vitest'

import { boundedDatabaseText, errorMessage, maxDiagnosticMessageBytes } from './diagnostics'

describe('channel gateway diagnostics', () => {
  it('preserves ordinary messages while replacing PostgreSQL-incompatible NULs', () => {
    expect(errorMessage(new Error('ordinary failure'))).toBe('ordinary failure')
    expect(errorMessage(new Error('bad\u0000value'))).toBe('bad\uFFFDvalue')
  })

  it('bounds multibyte messages without splitting UTF-8', () => {
    const bounded = boundedDatabaseText('界'.repeat(maxDiagnosticMessageBytes))

    expect(Buffer.byteLength(bounded)).toBeLessThanOrEqual(maxDiagnosticMessageBytes)
    expect(bounded.endsWith('…')).toBe(true)
    expect(bounded).not.toContain('\uFFFD')
  })

  it('handles values whose string conversion throws', () => {
    const value = {
      toString(): string {
        throw new Error('cannot stringify')
      },
    }

    expect(errorMessage(value)).toBe('unknown error')
  })

  it('handles Error objects with hostile non-string message properties', () => {
    const numericMessage = new Error('replaced')
    Object.defineProperty(numericMessage, 'message', { value: 42 })
    expect(errorMessage(numericMessage)).toBe('42')

    const throwingMessage = new Error('replaced')
    Object.defineProperty(throwingMessage, 'message', {
      get(): never {
        throw new Error('cannot read message')
      },
    })
    expect(errorMessage(throwingMessage)).toBe('unknown error')
  })
})
