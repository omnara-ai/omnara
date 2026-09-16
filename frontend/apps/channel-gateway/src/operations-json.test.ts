import { describe, expect, it, vi } from 'vitest'

import {
  capabilityKey,
  isReadOnlyOperation,
  parseOperation,
  serializeOperationResult,
} from './operations-json'

const capability = { connector_key: 'omnara', provider: 'slack' }
const scope = {
  project_id: 'project',
  integration_app_id: 'app',
  integration_install_id: 'install',
}
const allowed = new Set([capabilityKey(capability)])
const resolveEnvelope = () => ({
  request_id: 'setup-request',
  capability,
  kind: 'resolve_address',
  scope,
  deadline: new Date(Date.now() + 5000).toISOString(),
  payload: { provider_ref: 'C123' },
})

describe('installation-scoped address resolution', () => {
  it('requires only real installation scope and preserves original payload JSON', () => {
    const raw = JSON.stringify(resolveEnvelope()).replace(
      '"C123"',
      '"C123","number":9007199254740993',
    )
    const parsed = parseOperation(raw, allowed, Date.now() + 10_000)
    expect(parsed.kind).toBe('resolve_address')
    expect(parsed.scope).toEqual(scope)
    expect(parsed.artifacts).toEqual([])
    expect(parsed.payloadJSON).toContain('9007199254740993')
  })

  it.each([
    { ...scope, project_id: '' },
    { integration_app_id: 'app', integration_install_id: 'install' },
    { project_id: 'project', integration_install_id: 'install' },
    { project_id: 'project', integration_app_id: 'app' },
    { ...scope, agent_id: 'agent' },
    { ...scope, channel_id: 'channel' },
    { ...scope, agent_id: null },
    { ...scope, provider_url: 'https://provider.invalid' },
  ])('rejects missing, invalid, or additional setup scope IDs', (invalidScope) => {
    expect(() =>
      parseOperation(
        JSON.stringify({ ...resolveEnvelope(), scope: invalidScope }),
        allowed,
        Infinity,
      ),
    ).toThrow('invalid channel operation')
  })

  it.each(['send', 'read', 'interaction'])(
    'still requires agent and channel scope for %s',
    (kind) => {
      const envelope = { ...resolveEnvelope(), kind }
      for (const invalidScope of [
        scope,
        { ...scope, agent_id: 'agent' },
        { ...scope, channel_id: 'channel' },
      ]) {
        expect(() =>
          parseOperation(JSON.stringify({ ...envelope, scope: invalidScope }), allowed, Infinity),
        ).toThrow()
      }
      expect(
        parseOperation(
          JSON.stringify({
            ...envelope,
            scope: { ...scope, agent_id: 'agent', channel_id: 'channel' },
          }),
          allowed,
          Infinity,
        ).kind,
      ).toBe(kind)
    },
  )

  it('rejects resolve artifacts before dispatch and classifies it as read-only', () => {
    expect(() =>
      parseOperation(
        JSON.stringify({
          ...resolveEnvelope(),
          artifacts: [{ id: 'artifact', filename: 'file.txt', content_type: 'text/plain' }],
        }),
        allowed,
        Infinity,
      ),
    ).toThrow()
    expect(isReadOnlyOperation('resolve_address')).toBe(true)
    expect(isReadOnlyOperation('read')).toBe(true)
    expect(isReadOnlyOperation('send')).toBe(false)
    expect(isReadOnlyOperation('interaction')).toBe(false)
  })
})

describe('operation result serialization', () => {
  it('bounds result serialization before visiting later result properties', () => {
    const late = vi.fn(() => 'never read')
    const payload = Object.defineProperty({ huge: 'x'.repeat(1024 * 1024 + 1) }, 'late', {
      enumerable: true,
      get: late,
    })
    expect(() => serializeOperationResult(payload)).toThrow('invalid channel operation')
    expect(late).not.toHaveBeenCalled()
    expect(() => serializeOperationResult({ escaped: '\u0000'.repeat(200_000) })).toThrow()
    expect(() =>
      serializeOperationResult({ nodes: Array.from({ length: 16_384 }, () => null) }),
    ).toThrow()
    expect(serializeOperationResult({ number: 1, text: 'résumé', array: [true, null] })).toBe(
      JSON.stringify({ number: 1, text: 'résumé', array: [true, null] }),
    )
  })
})
