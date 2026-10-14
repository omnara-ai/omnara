/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type CurrentUser } from '@omnara/sdk'
import { getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, Suspense } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { DeleteAccountSection } from '@/components/settings/DeleteAccountSection'
import { DeleteOrganizationSection } from '@/components/settings/DeleteOrganizationSection'
import { useActiveOrg } from '@/lib/use-active-org'
import { AccountSettingsPage } from '@/routes/AccountSettingsPage'
import { emptyResponse, fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const apiBase = 'https://omnara.test/api/v1'
const acme = currentUserOrg({ id: fakeId('org'), name: 'Acme', role: 'owner' })
const other = currentUserOrg({ id: 'org_other', name: 'Other', role: 'member' })

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

beforeEach(() => {
  restoreActEnvironment = enableReactActEnvironment()
  window.localStorage.clear()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restoreActEnvironment()
})

async function flush() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function ActiveOrgName() {
  const { activeOrg } = useActiveOrg()
  return <p data-testid="active-org">{activeOrg.name}</p>
}

async function render(me: CurrentUser, initialPath: string, routes: FakeRoute[]) {
  const api = fakeApi(routes)
  const client = createOmnaraClient({ baseUrl: apiBase })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const meKey = getCurrentUserQueryKey({ client })
  queryClient.setQueryData(meKey, me)

  const rootRoute = createRootRoute()
  const page = (children: React.ReactNode) => () => (
    <ActiveOrgProvider>
      <ActiveOrgName />
      {children}
    </ActiveOrgProvider>
  )
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({ getParentRoute: () => rootRoute, path: '/', component: page(null) }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/settings',
        component: page(<DeleteOrganizationSection />),
      }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/user/account',
        component: AccountSettingsPage,
      }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/account-section',
        component: () => <DeleteAccountSection />,
      }),
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/onboarding',
        component: () => <p>onboarding</p>,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  })

  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <Suspense fallback={null}>
            <RouterProvider router={router} />
          </Suspense>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  await flush()
  return { api, router, queryClient, meKey }
}

function buttons(name: string) {
  return [...document.querySelectorAll('button')].filter((item) => item.textContent.trim() === name)
}

function dialog() {
  const element = document.querySelector('[role="dialog"]')
  if (!element) throw new Error('Missing dialog')
  return element
}

function confirmButton(name: string) {
  const match = [...dialog().querySelectorAll('button')].find(
    (item) => item.textContent.trim() === name,
  )
  if (!match) throw new Error(`Missing dialog button: ${name}`)
  return match
}

async function openDialog(name: string) {
  const trigger = buttons(name)[0]
  if (!trigger) throw new Error(`Missing trigger: ${name}`)
  act(() => {
    trigger.click()
  })
  await flush()
}

async function typeConfirmation(value: string) {
  await act(async () => {
    const input = document.getElementById('type-to-confirm')
    if (!(input instanceof HTMLInputElement)) throw new Error('Missing confirmation input')
    const descriptor = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')
    if (!descriptor?.set) throw new Error('Missing native field setter')
    descriptor.set.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await Promise.resolve()
  })
}

async function confirm(name: string) {
  act(() => {
    confirmButton(name).click()
  })
  await flush()
  await flush()
}

describe('DeleteOrganizationSection', () => {
  it('only lets owners start a delete', async () => {
    await render(currentUser([{ ...acme, role: 'admin' }]), '/settings', [])

    expect(buttons('Delete organization')[0]?.disabled).toBe(true)
    expect(container.textContent).toContain('Only organization owners can delete')
  })

  it('requires the org name, deletes, and switches to a remaining org', async () => {
    const path = `/api/v1/orgs/${acme.id}`
    const { api, router, queryClient, meKey } = await render(
      currentUser([acme, other]),
      '/settings',
      [
        { method: 'DELETE', path, respond: () => emptyResponse() },
        {
          method: 'GET',
          path: '/api/v1/me',
          respond: () => jsonResponse(currentUser([other])),
        },
      ],
    )
    await openDialog('Delete organization')
    expect(confirmButton('Delete organization').disabled).toBe(true)

    await typeConfirmation('acme')
    expect(confirmButton('Delete organization').disabled).toBe(true)
    await typeConfirmation('Acme')
    expect(confirmButton('Delete organization').disabled).toBe(false)

    await confirm('Delete organization')

    expect(api.requestsTo('DELETE', path)).toHaveLength(1)
    expect(router.state.location.pathname).toBe('/')
    expect(document.querySelector('[data-testid="active-org"]')?.textContent).toBe('Other')
    expect(queryClient.getQueryData<CurrentUser>(meKey)?.orgs.map((org) => org.id)).toEqual([
      other.id,
    ])
  })

  it('sends the user to onboarding after deleting their last org', async () => {
    const { router } = await render(currentUser([acme]), '/settings', [
      { method: 'DELETE', path: `/api/v1/orgs/${acme.id}`, respond: () => emptyResponse() },
      { method: 'GET', path: '/api/v1/me', respond: () => jsonResponse(currentUser([])) },
    ])
    await openDialog('Delete organization')
    await typeConfirmation('Acme')
    await confirm('Delete organization')

    expect(router.state.location.pathname).toBe('/onboarding')
  })

  it('keeps the dialog open and shows the API error on failure', async () => {
    const { router } = await render(currentUser([acme]), '/settings', [
      {
        method: 'DELETE',
        path: `/api/v1/orgs/${acme.id}`,
        respond: () => jsonResponse({ code: 'conflict', error: 'Org is busy' }, 409),
      },
    ])
    await openDialog('Delete organization')
    await typeConfirmation('Acme')
    await confirm('Delete organization')

    expect(dialog().querySelector('[role="alert"]')?.textContent).toBe('Org is busy')
    expect(router.state.location.pathname).toBe('/settings')
  })
})

describe('DeleteAccountSection', () => {
  it('lets a user with no orgs delete their account from the account page', async () => {
    window.history.replaceState(null, '', '/user/account')
    const me = currentUser([])
    const { api } = await render(me, '/user/account', [
      { method: 'DELETE', path: '/api/v1/me', respond: () => emptyResponse() },
    ])
    await openDialog('Delete account')
    expect(confirmButton('Delete account').disabled).toBe(true)
    await typeConfirmation(me.user.email)

    await confirm('Delete account')

    expect(api.requestsTo('DELETE', '/api/v1/me')).toHaveLength(1)
    expect(window.location.pathname).toBe('/login')
  })

  it('blocks deletion and names the orgs the user still owns', async () => {
    const beta = currentUserOrg({ id: 'org_beta', name: 'Beta', role: 'owner' })
    await render(currentUser([acme, other, beta]), '/account-section', [])

    expect(buttons('Delete account')[0]?.disabled).toBe(true)
    expect(container.textContent).toContain(
      'You own Acme and Beta. Delete those organizations from organization settings before deleting your account.',
    )
    expect([...container.querySelectorAll('strong')].map((item) => item.textContent)).toEqual([
      'Acme',
      'Beta',
    ])
  })

  it('keeps the dialog open and shows the API error on failure', async () => {
    window.history.replaceState(null, '', '/account-section')
    const me = currentUser([other])
    await render(me, '/account-section', [
      {
        method: 'DELETE',
        path: '/api/v1/me',
        respond: () => jsonResponse({ code: 'conflict', error: 'Account is busy' }, 409),
      },
    ])
    await openDialog('Delete account')
    await typeConfirmation(me.user.email)
    await confirm('Delete account')

    expect(dialog().querySelector('[role="alert"]')?.textContent).toBe('Account is busy')
    expect(window.location.pathname).toBe('/account-section')
  })
})
