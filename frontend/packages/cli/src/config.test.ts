import { describe, expect, it } from 'vitest'

import { DEFAULT_API_URL, DEFAULT_APP_URL, resolveUrls } from './config.ts'

describe('resolveUrls', () => {
  it('defaults to the API host for requests and the app host for the browser', () => {
    expect(resolveUrls({}, {})).toEqual({ apiUrl: DEFAULT_API_URL, appUrl: DEFAULT_APP_URL })
  })

  it('prefers environment variables over the config file', () => {
    expect(
      resolveUrls(
        { api_url: 'https://api.file.example', app_url: 'https://file.example' },
        { OMNARA_API_URL: 'https://api.env.example', OMNARA_APP_URL: 'https://env.example' },
      ),
    ).toEqual({ apiUrl: 'https://api.env.example', appUrl: 'https://env.example' })
  })

  it('uses a self-hosted legacy base_url for both URLs', () => {
    expect(resolveUrls({ base_url: 'https://self-hosted.example' }, {})).toEqual({
      apiUrl: 'https://self-hosted.example',
      appUrl: 'https://self-hosted.example',
    })
    expect(
      resolveUrls({ base_url: 'https://self-hosted.example', api_url: 'https://api.example' }, {}),
    ).toEqual({ apiUrl: 'https://api.example', appUrl: 'https://self-hosted.example' })
  })

  it('ignores a legacy base_url that points at the hosted web app', () => {
    expect(resolveUrls({ base_url: DEFAULT_APP_URL }, {})).toEqual({
      apiUrl: DEFAULT_API_URL,
      appUrl: DEFAULT_APP_URL,
    })
  })
})
