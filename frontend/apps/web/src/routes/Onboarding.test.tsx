/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type OrgInvitation } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, Suspense } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { Onboarding } from '@/routes/Onboarding'
import { type FakeApi, fakeApi, type FakeRoute, jsonResponse, neverResponds } from '@/test/fake-api'
import { fakeId, orgInvitation } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const apiBase = 'https://omnara.test/api/v1'

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

function element(selector: string): Element {
  const match = container.querySelector(selector)
  if (!match) throw new Error(`Missing test element: ${selector}`)
  return match
}

function inputElement(selector: string): HTMLInputElement {
  const match = element(selector)
  if (!(match instanceof HTMLInputElement)) throw new Error(`Not an input: ${selector}`)
  return match
}

function buttonElement(selector: string): HTMLButtonElement {
  const match = element(selector)
  if (!(match instanceof HTMLButtonElement)) throw new Error(`Not a button: ${selector}`)
  return match
}

function htmlElement(selector: string): HTMLElement {
  const match = element(selector)
  if (!(match instanceof HTMLElement)) throw new Error(`Not an HTML element: ${selector}`)
  return match
}

async function flush() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function onboardingApi({
  invitations = [],
  createOrganization = () => jsonResponse(createdOrganization, 201),
}: {
  invitations?: OrgInvitation[]
  createOrganization?: FakeRoute['respond']
} = {}): FakeApi {
  return fakeApi([
    {
      method: 'GET',
      path: '/api/v1/invitations',
      respond: () => jsonResponse({ data: invitations, next_cursor: null }),
    },
    { method: 'POST', path: '/api/v1/orgs', respond: createOrganization },
    {
      method: 'POST',
      path: `/api/v1/invitations/${fakeId('oinv')}/accept`,
      respond: () => jsonResponse(invitations[0] ?? null),
    },
  ])
}

const timestamp = '2026-01-01T00:00:00Z'

const createdOrganization = {
  org: { id: fakeId('org'), name: 'Acme Inc.', created_at: timestamp, updated_at: timestamp },
  project: {
    id: fakeId('proj'),
    org_id: fakeId('org'),
    name: 'default',
    created_at: timestamp,
    updated_at: timestamp,
  },
  membership: {
    org_id: fakeId('org'),
    user_id: fakeId('usr'),
    role: 'owner',
    created_at: timestamp,
  },
}

async function renderOnboarding(api: FakeApi) {
  const client = createOmnaraClient({ baseUrl: apiBase })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <Suspense fallback={null}>
            <Onboarding />
          </Suspense>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })
  await flush()
}

function setOrganizationName(name: string) {
  const input = inputElement('#org-name')
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.bind(
    input,
  )
  if (!setValue) throw new Error('Missing HTMLInputElement value setter')

  act(() => {
    setValue(name)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function submitOrganization() {
  await act(async () => {
    element('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
  })
  await flush()
}

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})

afterAll(() => {
  restoreActEnvironment()
})

beforeEach(() => {
  window.history.replaceState(null, '', '/onboarding')
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

describe('Onboarding organization creation', () => {
  it('navigates home after creating the organization successfully', async () => {
    const api = onboardingApi()
    await renderOnboarding(api)
    setOrganizationName('Acme Inc.')

    await submitOrganization()

    expect(api.requestsTo('POST', '/api/v1/orgs')).toHaveLength(1)
    expect(window.location.pathname).toBe('/')
  })

  it('returns to the device approval page it was sent from after creating the organization', async () => {
    window.history.replaceState(
      null,
      '',
      '/onboarding?return_to=%2Fdevice%3Fuser_code%3DABCDE-F1234',
    )
    await renderOnboarding(onboardingApi())

    expect(container.textContent).toContain(
      'Create your organization, then we will bring you back to approve the CLI login.',
    )

    setOrganizationName('Acme Inc.')
    await submitOrganization()

    expect(window.location.pathname).toBe('/device')
    expect(window.location.search).toBe('?user_code=ABCDE-F1234')
  })

  it('returns to the device approval page after accepting an invitation', async () => {
    window.history.replaceState(
      null,
      '',
      '/onboarding?return_to=%2Fdevice%3Fuser_code%3DABCDE-F1234',
    )
    const api = onboardingApi({ invitations: [orgInvitation()] })
    await renderOnboarding(api)

    const accept = Array.from(container.querySelectorAll('button')).find(
      (candidate) => candidate.textContent.trim() === 'Accept',
    )
    if (!accept) throw new Error('Missing Accept button')
    await act(async () => {
      accept.click()
      await Promise.resolve()
    })
    await flush()

    expect(api.requestsTo('POST', `/api/v1/invitations/${fakeId('oinv')}/accept`)).toHaveLength(1)
    expect(window.location.pathname).toBe('/device')
    expect(window.location.search).toBe('?user_code=ABCDE-F1234')
  })

  it('ignores an unsafe return destination', async () => {
    const host = window.location.host
    window.history.replaceState(
      null,
      '',
      '/onboarding?return_to=https%3A%2F%2Fevil.example%2Fsteal',
    )
    await renderOnboarding(onboardingApi())
    setOrganizationName('Acme Inc.')

    await submitOrganization()

    expect(window.location.host).toBe(host)
    expect(window.location.pathname).toBe('/')
  })

  it('does not throw when a translator wraps the label before the pending update', async () => {
    await renderOnboarding(onboardingApi({ createOrganization: neverResponds }))
    setOrganizationName('Acme Inc.')

    const button = buttonElement('button[type="submit"]')
    const loadingIcon = htmlElement('[data-slot="button-loading-icon"]')
    const label = htmlElement('[data-slot="button-label"]')
    const originalLabel = label.firstChild
    if (!originalLabel) throw new Error('Missing button label text node')

    expect(button.disabled).toBe(false)
    expect(button.getAttribute('aria-busy')).toBeNull()
    expect(loadingIcon.hidden).toBe(true)
    expect(loadingIcon.getAttribute('aria-hidden')).toBe('true')
    expect(label.textContent).toBe('Create organization')

    const translatedLabel = document.createElement('span')
    translatedLabel.lang = 'es'
    translatedLabel.textContent = 'Crear organización'
    label.replaceChild(translatedLabel, originalLabel)

    await expect(submitOrganization()).resolves.toBeUndefined()

    expect(button.disabled).toBe(true)
    expect(button.getAttribute('aria-busy')).toBe('true')
    expect(loadingIcon.hidden).toBe(false)
    expect(label.firstChild).toBe(translatedLabel)
  })
})
