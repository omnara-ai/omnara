import { describe, expect, it, vi } from 'vitest'

import {
  createTokenResolver,
  DEFAULT_API_URL,
  DEFAULT_ISSUER_URL,
  migrateLegacyBaseUrl,
  resolveUrls,
} from './config.ts'

describe('resolveUrls', () => {
  it('defaults to the hosted API root for requests and the hosted app for the issuer', () => {
    expect(resolveUrls({}, {})).toEqual({ apiUrl: DEFAULT_API_URL, issuerUrl: DEFAULT_ISSUER_URL })
  })

  it('prefers environment variables over the config file', () => {
    expect(
      resolveUrls(
        { api_url: 'https://file.example/api/v1', issuer_url: 'https://file.example' },
        { OMNARA_API_URL: 'https://env.example/api/v1', OMNARA_ISSUER_URL: 'https://env.example' },
      ),
    ).toEqual({ apiUrl: 'https://env.example/api/v1', issuerUrl: 'https://env.example' })
  })

  it('keeps the hosted API root when only the issuer is overridden', () => {
    expect(resolveUrls({ issuer_url: 'https://self-hosted.example' }, {})).toEqual({
      apiUrl: DEFAULT_API_URL,
      issuerUrl: 'https://self-hosted.example',
    })
    expect(resolveUrls({}, { OMNARA_ISSUER_URL: 'https://self-hosted.example' })).toEqual({
      apiUrl: DEFAULT_API_URL,
      issuerUrl: 'https://self-hosted.example',
    })
  })

  it('keeps the hosted issuer when only the API root is overridden', () => {
    expect(resolveUrls({ api_url: 'https://self-hosted.example/api/v1' }, {})).toEqual({
      apiUrl: 'https://self-hosted.example/api/v1',
      issuerUrl: DEFAULT_ISSUER_URL,
    })
  })

  it('keeps an explicit api_url next to a self-hosted issuer', () => {
    expect(
      resolveUrls(
        {
          issuer_url: 'https://self-hosted.example',
          api_url: 'https://api.self-hosted.example/v1',
        },
        {},
      ),
    ).toEqual({
      apiUrl: 'https://api.self-hosted.example/v1',
      issuerUrl: 'https://self-hosted.example',
    })
  })
})

describe('migrateLegacyBaseUrl', () => {
  it('leaves configs without base_url alone', () => {
    expect(migrateLegacyBaseUrl({ token: 'omnara_pat_v1_test' })).toBeUndefined()
  })

  it('drops the legacy base_url and preserves current fields', () => {
    expect(
      migrateLegacyBaseUrl({
        base_url: 'https://old.example',
        api_url: 'https://api.new.example/v1',
        issuer_url: 'https://app.new.example',
        token: 'omnara_pat_v1_test',
      }),
    ).toEqual({
      api_url: 'https://api.new.example/v1',
      issuer_url: 'https://app.new.example',
      token: 'omnara_pat_v1_test',
    })
  })
})

describe('createTokenResolver', () => {
  it('returns the saved token without prompting', async () => {
    const login = vi.fn(() => Promise.resolve('omnara_pat_v1_new'))
    const resolve = createTokenResolver({
      savedToken: 'omnara_pat_v1_saved',
      canPrompt: () => true,
      login,
    })

    await expect(resolve()).resolves.toBe('omnara_pat_v1_saved')
    expect(login).not.toHaveBeenCalled()
  })

  it('logs in once when no token is saved and reuses the result', async () => {
    const login = vi.fn(() => Promise.resolve('omnara_pat_v1_new'))
    const resolve = createTokenResolver({ savedToken: undefined, canPrompt: () => true, login })

    const [first, second] = await Promise.all([resolve(), resolve()])
    await expect(resolve()).resolves.toBe('omnara_pat_v1_new')
    expect(first).toBe('omnara_pat_v1_new')
    expect(second).toBe('omnara_pat_v1_new')
    expect(login).toHaveBeenCalledTimes(1)
  })

  it('fails with a login hint when it cannot prompt', async () => {
    const login = vi.fn(() => Promise.resolve('omnara_pat_v1_new'))
    const resolve = createTokenResolver({ savedToken: undefined, canPrompt: () => false, login })

    await expect(resolve()).rejects.toThrow(
      "not logged in: run 'omnara login' or set OMNARA_API_KEY",
    )
    expect(login).not.toHaveBeenCalled()
  })
})
