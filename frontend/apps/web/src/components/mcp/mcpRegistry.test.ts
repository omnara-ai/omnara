import { describe, expect, it } from 'vitest'

import {
  githubNamespaceUser,
  registryServerDomain,
  registryServerEntries,
  registryServerIconCandidates,
  registryServerIconSrc,
  registryServerRemoteUrl,
  registryServerSearchFilters,
  registryServerShortName,
  registryServerSuggestedName,
} from '@/components/mcp/mcpRegistry'

describe('registryServerShortName', () => {
  it('uses the last path segment of the registry name', () => {
    expect(registryServerShortName({ name: 'io.github.github/github-mcp-server' })).toBe(
      'github-mcp-server',
    )
    expect(registryServerShortName({ name: 'com.notion/mcp' })).toBe('mcp')
    expect(registryServerShortName({ name: 'ai.example/Weird Name!' })).toBe('Weird-Name')
  })
})

describe('registryServerSuggestedName', () => {
  it('produces a valid MCP server name or nothing', () => {
    expect(registryServerSuggestedName({ name: 'io.github.foo/my_server.v2' })).toBe('my-server-v2')
    expect(registryServerSuggestedName({ name: 'ai.example/42-weird name!' })).toBe('weird-name')
    expect(registryServerSuggestedName({ name: 'ai.example/1234' })).toBe('')
    expect(
      registryServerSuggestedName({ name: `ai.example/${'a'.repeat(40)}-tail` }).length,
    ).toBeLessThanOrEqual(32)
  })
})

describe('registryServerRemoteUrl', () => {
  it('only considers streamable-http remotes', () => {
    expect(
      registryServerRemoteUrl({
        remotes: [
          { type: 'sse', url: 'https://a.example/sse' },
          { type: 'streamable-http', url: 'https://a.example/mcp' },
        ],
      }),
    ).toBe('https://a.example/mcp')
    expect(
      registryServerRemoteUrl({ remotes: [{ type: 'sse', url: 'https://a.example/sse' }] }),
    ).toBe('')
  })
})

describe('registryServerEntries', () => {
  it('emits one entry per streamable-http remote and drops sse', () => {
    const sse = { type: 'sse', url: 'https://docs.mcp.cloudflare.com/sse' }
    const cloudflare = {
      name: 'com.cloudflare/mcp',
      description: 'Cloudflare',
      version: '1',
      status: 'active',
      updated_at: '2026-08-20T00:00:00Z',
      icons: [],
      remotes: [
        { type: 'streamable-http', url: 'https://docs.mcp.cloudflare.com/mcp' },
        sse,
        { type: 'streamable-http', url: 'https://bindings.mcp.cloudflare.com/mcp' },
      ],
    }
    const sseOnly = { ...cloudflare, name: 'com.example/sse', remotes: [sse] }
    const entries = registryServerEntries([cloudflare, sseOnly])
    expect(entries.map((entry) => entry.remote.url)).toEqual([
      'https://docs.mcp.cloudflare.com/mcp',
      'https://bindings.mcp.cloudflare.com/mcp',
    ])
    expect(new Set(entries.map((entry) => entry.key)).size).toBe(2)
    expect(entries.every((entry) => entry.server === cloudflare)).toBe(true)
  })
})

describe('registryServerIconSrc', () => {
  it('prefers a themed icon, then an untitled one', () => {
    const dark = { src: 'https://x/dark.png', theme: 'dark' }
    const icons = [
      dark,
      { src: 'https://x/any.png' },
      { src: 'https://x/light.png', theme: 'light' },
    ]
    expect(registryServerIconSrc({ icons }, 'dark')).toBe('https://x/dark.png')
    expect(registryServerIconSrc({ icons }, 'light')).toBe('https://x/light.png')
    expect(registryServerIconSrc({ icons: [dark] }, 'light')).toBe('https://x/dark.png')
    expect(registryServerIconSrc({ icons: [] })).toBeNull()
    expect(registryServerIconSrc(null)).toBeNull()
  })
})

describe('registryServerSearchFilters', () => {
  it('maps the name box to q and drops blanks', () => {
    expect(registryServerSearchFilters(' linear ')).toEqual({ q: 'linear' })
    expect(registryServerSearchFilters('  ')).toEqual({})
  })
})

describe('registryServerDomain', () => {
  it('reverses the reverse-DNS namespace', () => {
    expect(registryServerDomain({ name: 'app.linear/linear' })).toBe('linear.app')
    expect(registryServerDomain({ name: 'com.cloudflare/mcp' })).toBe('cloudflare.com')
    expect(registryServerDomain({ name: 'dev.mcp.sentry/sentry' })).toBe('sentry.mcp.dev')
    expect(registryServerDomain({ name: 'localonly/thing' })).toBeNull()
    expect(registryServerDomain({ name: 'com.bad_ns/thing' })).toBeNull()
  })
})

describe('registryServerIconCandidates', () => {
  it('orders registry icon, namespace favicon, then remote origin favicon', () => {
    expect(
      registryServerIconCandidates(
        { name: 'app.linear/linear', icons: [{ src: 'https://cdn/linear.png' }] },
        'https://mcp.linear.app/mcp',
      ),
    ).toEqual([
      'https://cdn/linear.png',
      'https://linear.app/favicon.ico',
      'https://mcp.linear.app/favicon.ico',
    ])
    expect(githubNamespaceUser({ name: 'io.github.getsentry/sentry-mcp' })).toBe('getsentry')
    expect(
      registryServerIconCandidates({ name: 'io.github.getsentry/sentry-mcp', icons: [] }),
    ).toEqual(['https://github.com/getsentry.png?size=64'])
    expect(registryServerIconCandidates(null, 'not a url')).toEqual([])
    expect(registryServerIconCandidates(null, 'ftp://x/y')).toEqual([])
  })
})
