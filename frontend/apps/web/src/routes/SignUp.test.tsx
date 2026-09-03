/** @vitest-environment happy-dom */

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { SignUp } from '@/routes/SignUp'
import { emptyResponse, fakeApi, jsonResponse } from '@/test/fake-api'
import { enableReactActEnvironment } from '@/test/react-act'

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

function signupRouter(search: string) {
  const rootRoute = createRootRoute()
  const signupRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/signup',
    component: SignUp,
  })
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    component: () => null,
  })
  return createRouter({
    routeTree: rootRoute.addChildren([signupRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: [`/signup${search}`] }),
  })
}

function linkHref(label: string) {
  return Array.from(container.querySelectorAll('a'))
    .find((link) => link.textContent === label)
    ?.getAttribute('href')
}

async function flush() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})

afterAll(() => {
  restoreActEnvironment()
})

beforeEach(() => {
  window.history.replaceState(null, '', '/signup?return_to=%2Fdevice%3Fuser_code%3DABCDE-F1234')
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
})

describe('signup continuation', () => {
  it('preserves the device approval destination for social and email signup', async () => {
    const api = fakeApi([
      {
        method: 'GET',
        path: '/api/auth/connectors',
        respond: () =>
          jsonResponse({
            connectors: [
              {
                slug: 'google',
                kind: 'oidc',
                display_name: 'Google',
                login_url: '/api/auth/google/login',
              },
            ],
          }),
      },
      { method: 'POST', path: '/api/auth/signup', respond: () => emptyResponse() },
    ])
    vi.stubGlobal('fetch', api.fetch)
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const router = signupRouter(window.location.search)

    act(() => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      )
    })
    await flush()
    await flush()

    const returnTo = '/device?user_code=ABCDE-F1234'
    const encodedReturnTo = 'return_to=%2Fdevice%3Fuser_code%3DABCDE-F1234'
    expect(container.querySelector('h1 + p')?.textContent).toContain(
      'Create an account to approve the CLI login',
    )
    expect(linkHref('Continue with Google')).toBe(`/api/auth/google/login?${encodedReturnTo}`)
    expect(linkHref('Sign in')).toBe(`/login?${encodedReturnTo}`)

    const input = container.querySelector('#email')
    if (!(input instanceof HTMLInputElement)) throw new Error('Missing email input')
    const setValue = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      'value',
    )?.set?.bind(input)
    if (!setValue) throw new Error('Missing HTMLInputElement value setter')
    act(() => {
      setValue('new@example.com')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => {
      container
        .querySelector('form')
        ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await Promise.resolve()
    })
    await flush()

    expect(api.requestsTo('POST', '/api/auth/signup').map((request) => request.body)).toEqual([
      { email: 'new@example.com', return_to: returnTo },
    ])
    expect(container.querySelector('h1 + p')?.textContent).toContain('within 15 minutes')
    expect(linkHref('Back to sign in')).toBe(`/login?${encodedReturnTo}`)
  })
})
