/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { OAuthAuthorize } from '@/routes/OAuthAuthorize'
import { type FakeApi, fakeApi, type FakeRoute, jsonResponse, neverResponds } from '@/test/fake-api'
import { enableReactActEnvironment } from '@/test/react-act'

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void
let assignedLocation: string | undefined

const query =
  '?response_type=code&client_id=https%3A%2F%2Fclient.example%2Fclient.json&redirect_uri=http%3A%2F%2F127.0.0.1%3A3000%2Fcallback&state=abc'

function oauthAuthorizeApi({
  pending = () =>
    jsonResponse({
      client_id: 'https://client.example/client.json',
      client_name: 'Example MCP Client',
      client_uri: 'https://client.example',
      redirect_uri: 'http://127.0.0.1:3000/callback',
      redirect_host: '127.0.0.1:3000',
      loopback: true,
      resource: 'https://omnara.test/mcp',
    }),
  approve = () => jsonResponse({ redirect_url: 'http://127.0.0.1:3000/callback?code=abc' }),
  deny = neverResponds,
}: {
  pending?: FakeRoute['respond']
  approve?: FakeRoute['respond']
  deny?: FakeRoute['respond']
} = {}): FakeApi {
  const api = fakeApi([
    { method: 'GET', path: '/api/auth/oauth/authorize/pending', respond: pending },
    { method: 'POST', path: '/api/auth/oauth/authorize/approve', respond: approve },
    { method: 'POST', path: '/api/auth/oauth/authorize/deny', respond: deny },
  ])
  vi.stubGlobal('fetch', api.fetch)
  return api
}

async function renderOAuthAuthorize() {
  await act(async () => {
    root.render(<OAuthAuthorize />)
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function button(label: string): HTMLButtonElement {
  const match = Array.from(container.querySelectorAll('button')).find(
    (candidate) => candidate.textContent.trim() === label,
  )
  if (!match) throw new Error(`Missing button: ${label}`)
  return match
}

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})

afterAll(() => {
  restoreActEnvironment()
})

beforeEach(() => {
  window.history.replaceState(null, '', `/oauth/authorize${query}`)
  assignedLocation = undefined
  vi.spyOn(window.location, 'assign').mockImplementation((url: string | URL) => {
    assignedLocation = String(url)
  })
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('OAuth authorization decisions', () => {
  it('shows the client and warns about loopback redirects', async () => {
    oauthAuthorizeApi()
    await renderOAuthAuthorize()

    expect(container.textContent).toContain('Example MCP Client')
    expect(container.textContent).toContain('127.0.0.1:3000')
    expect(container.textContent).toContain('redirects to a program running on your own computer')
  })

  it('approves with the original query and follows the redirect', async () => {
    const api = oauthAuthorizeApi()
    await renderOAuthAuthorize()

    await act(async () => {
      button('Approve').click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    expect(
      api.requestsTo('POST', '/api/auth/oauth/authorize/approve').map((request) => request.body),
    ).toEqual([{ query }])
    expect(assignedLocation).toBe('http://127.0.0.1:3000/callback?code=abc')
    expect(container.textContent).toContain('Returning to the application')
  })

  it('follows the error redirect when the request is rejected for the client', async () => {
    oauthAuthorizeApi({
      pending: () =>
        jsonResponse(
          {
            error: 'invalid_target',
            error_description: 'resource is required',
            redirect_url: 'http://127.0.0.1:3000/callback?error=invalid_target',
          },
          400,
        ),
    })
    await renderOAuthAuthorize()

    expect(assignedLocation).toBe('http://127.0.0.1:3000/callback?error=invalid_target')
  })

  it('shows client errors that cannot be redirected', async () => {
    oauthAuthorizeApi({
      pending: () =>
        jsonResponse(
          { error: 'invalid_client', error_description: 'client metadata returned status 404' },
          400,
        ),
    })
    await renderOAuthAuthorize()

    expect(assignedLocation).toBeUndefined()
    expect(container.textContent).toContain('client metadata returned status 404')
  })
})
