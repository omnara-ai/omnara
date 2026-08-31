import { describe, expect, it } from 'vitest'

import { DEFAULT_API_URL, DEFAULT_ISSUER_URL, migrateLegacyBaseUrl, resolveUrls } from './config.ts'

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

  it('drops a legacy base_url and keeps the rest of the config', () => {
    expect(
      migrateLegacyBaseUrl({
        base_url: 'https://self-hosted.example',
        token: 'omnara_pat_v1_test',
      }),
    ).toEqual({
      token: 'omnara_pat_v1_test',
    })
  })

  it('keeps an issuer_url that was already saved', () => {
    expect(
      migrateLegacyBaseUrl({ base_url: 'https://old.example', issuer_url: 'https://new.example' }),
    ).toEqual({ issuer_url: 'https://new.example' })
  })
})
