import { type APIRequestContext, type Page, type Route } from '@playwright/test'

import { requiredEnvironmentVariable } from './fixtures'

function localTransport(url: string, headers: Headers) {
  const origin = new URL(requiredEnvironmentVariable('OMNARA_WEB_E2E_BASE_URL'))
  const target = new URL(url, origin)
  if (target.origin !== origin.origin || target.protocol !== 'https:')
    throw new Error('Local test transport requires the configured HTTPS origin')

  headers.set('Host', target.host)
  target.hostname = '127.0.0.1'
  return {
    url: target.toString(),
    headers: Object.fromEntries(headers),
    maxRedirects: 0,
  }
}

export async function fetchLocalAPI(
  page: Page,
  url: string,
  options: Parameters<APIRequestContext['fetch']>[1] = {},
) {
  const headers = new Headers(options.headers)
  if (!headers.has('Cookie')) {
    const cookies = await page.context().cookies(url)
    headers.set('Cookie', cookies.map(({ name, value }) => `${name}=${value}`).join('; '))
  }
  const transport = localTransport(url, headers)
  return page.request.fetch(transport.url, { ...options, ...transport })
}

export async function fetchLocalRoute(route: Route, options: Parameters<Route['fetch']>[0] = {}) {
  const headers = new Headers(await route.request().allHeaders())
  for (const [name, value] of new Headers(options.headers)) headers.set(name, value)
  return route.fetch({ ...options, ...localTransport(route.request().url(), headers) })
}
