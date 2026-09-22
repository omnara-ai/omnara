import { EventEmitter } from 'node:events'

import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { installAppFailureTracking, installFailureTracking } from '../../e2e/fixtures'

const origin = 'https://app.omnara.test'
const appPath = '/api/v1/orgs/org_test/projects/proj_test/apps/app_aaaaaaaaaaaaaaaaaaaaaaaaaa'

beforeEach(() => {
  vi.stubEnv('OMNARA_WEB_E2E_BASE_URL', origin)
})

afterEach(() => {
  vi.unstubAllEnvs()
})

function request(method: string, path = appPath, error = 'net::ERR_ABORTED') {
  return {
    method: () => method,
    url: () => `${origin}${path}`,
    failure: () => ({ errorText: error }),
  }
}

function tracker() {
  const page = new EventEmitter()
  const failures = installAppFailureTracking(page)
  return { page, failures }
}

it('ignores a canceled app read but records an unexpected canceled read', () => {
  const { page, failures } = tracker()
  page.emit('requestfailed', request('GET'))
  expect(failures).toEqual([])
  page.emit('requestfailed', request('GET', '/unexpected'))
  expect(failures).toEqual([`request: GET ${origin}/unexpected (net::ERR_ABORTED)`])
})

it('ignores only canceled POST reads for agent config tools', () => {
  const { page, failures } = tracker()
  const path = '/api/v1/orgs/org_test/projects/proj_test/agent-configs/tools'
  page.emit('requestfailed', request('POST', path))
  expect(failures).toEqual([])
  page.emit('requestfailed', request('POST', path, 'net::ERR_CONNECTION_RESET'))
  expect(failures).toEqual([`request: POST ${origin}${path} (net::ERR_CONNECTION_RESET)`])
})

it.each(['POST', 'PUT', 'PATCH', 'DELETE'])(
  'records an aborted %s to an exempt read URL',
  (method) => {
    const { page, failures } = tracker()
    page.emit('requestfailed', request(method))
    expect(failures).toEqual([`request: ${method} ${origin}${appPath} (net::ERR_ABORTED)`])
  },
)

it.each(['POST', 'PUT', 'PATCH'])('records an aborted %s even after a 204', (method) => {
  const { page, failures } = tracker()
  const write = request(method)
  page.emit('response', { request: () => write, status: () => 204, url: write.url })
  page.emit('requestfailed', write)
  expect(failures).toEqual([`request: ${method} ${origin}${appPath} (net::ERR_ABORTED)`])
})

it('ignores an aborted DELETE only after that request received a 204', () => {
  const { page, failures } = tracker()
  const deletion = request('DELETE')
  page.emit('response', { request: () => deletion, status: () => 204, url: deletion.url })
  page.emit('requestfailed', deletion)
  expect(failures).toEqual([])
})

it.each([200, 404, 500])('records an aborted DELETE after a %i', (status) => {
  const { page, failures } = tracker()
  const deletion = request('DELETE')
  page.emit('response', { request: () => deletion, status: () => status, url: deletion.url })
  page.emit('requestfailed', deletion)
  expect(failures).toEqual([
    ...(status >= 400 ? [`response: ${status} ${appPath}`] : []),
    `request: DELETE ${origin}${appPath} (net::ERR_ABORTED)`,
  ])
})

it('does not borrow a 204 from a different request to the same URL', () => {
  const { page, failures } = tracker()
  const completed = request('DELETE')
  page.emit('response', { request: () => completed, status: () => 204, url: completed.url })
  page.emit('requestfailed', request('DELETE'))
  expect(failures).toEqual([`request: DELETE ${origin}${appPath} (net::ERR_ABORTED)`])
})

it('records other DELETE failures even after a 204', () => {
  const { page, failures } = tracker()
  const deletion = request('DELETE', appPath, 'net::ERR_CONNECTION_RESET')
  page.emit('response', { request: () => deletion, status: () => 204, url: deletion.url })
  page.emit('requestfailed', deletion)
  expect(failures).toEqual([`request: DELETE ${origin}${appPath} (net::ERR_CONNECTION_RESET)`])
})

it('cannot use a broad ignore pattern to suppress a write or an uncanceled read failure', () => {
  const page = new EventEmitter()
  const failures = installFailureTracking(page, [/request:/])
  page.emit('requestfailed', request('PUT'))
  page.emit('requestfailed', request('POST'))
  page.emit('requestfailed', request('GET', appPath, 'net::ERR_CONNECTION_RESET'))
  expect(failures).toEqual([
    `request: PUT ${origin}${appPath} (net::ERR_ABORTED)`,
    `request: POST ${origin}${appPath} (net::ERR_ABORTED)`,
    `request: GET ${origin}${appPath} (net::ERR_CONNECTION_RESET)`,
  ])
})
