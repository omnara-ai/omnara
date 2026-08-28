import { describe, expect, it } from 'vitest'

import {
  DEFAULT_API_URL,
  DEFAULT_ISSUER_URL,
  migrateLegacyBaseUrl,
  resolveUrls,
  selfHostedApiUrl,
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

  it('derives the API root from a self-hosted issuer when api_url is not set', () => {
    expect(resolveUrls({ issuer_url: 'https://self-hosted.example/' }, {})).toEqual({
      apiUrl: 'https://self-hosted.example/api/v1',
      issuerUrl: 'https://self-hosted.example/',
    })
    expect(resolveUrls({}, { OMNARA_ISSUER_URL: 'https://self-hosted.example' })).toEqual({
      apiUrl: 'https://self-hosted.example/api/v1',
      issuerUrl: 'https://self-hosted.example',
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

describe('selfHostedApiUrl', () => {
  it('appends the /api/v1 mount to the origin', () => {
    expect(selfHostedApiUrl('https://self-hosted.example')).toBe(
      'https://self-hosted.example/api/v1',
    )
    expect(selfHostedApiUrl('http://localhost:8080/')).toBe('http://localhost:8080/api/v1')
  })
})

describe('migrateLegacyBaseUrl', () => {
  it('leaves configs without base_url alone', () => {
    expect(migrateLegacyBaseUrl({ token: 'omnara_pat_v1_test' })).toBeUndefined()
  })

  it('drops a legacy base_url that points at the hosted web app', () => {
    expect(
      migrateLegacyBaseUrl({ base_url: DEFAULT_ISSUER_URL, token: 'omnara_pat_v1_test' }),
    ).toEqual({
      token: 'omnara_pat_v1_test',
    })
  })

  it('turns a self-hosted base_url into the issuer_url', () => {
    expect(migrateLegacyBaseUrl({ base_url: 'https://self-hosted.example' })).toEqual({
      issuer_url: 'https://self-hosted.example',
    })
  })

  it('keeps an issuer_url that was already saved', () => {
    expect(
      migrateLegacyBaseUrl({ base_url: 'https://old.example', issuer_url: 'https://new.example' }),
    ).toEqual({ issuer_url: 'https://new.example' })
  })
})
