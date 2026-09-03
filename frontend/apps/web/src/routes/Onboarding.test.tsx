/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { Onboarding } from '@/routes/Onboarding'

const hooks = vi.hoisted(() => ({
  acceptInvitation: vi.fn(),
  createOrganization: vi.fn(),
  declineInvitation: vi.fn(),
  refetchInvitations: vi.fn(),
  pendingInvitations: [] as unknown[],
}))

vi.mock('@omnara/react', () => ({
  useAcceptInvitation: () => ({ mutateAsync: hooks.acceptInvitation }),
  useCreateOrganization: () => ({ mutateAsync: hooks.createOrganization }),
  useDeclineInvitation: () => ({ mutateAsync: hooks.declineInvitation }),
  usePendingInvitations: () => ({
    data: { data: hooks.pendingInvitations },
    refetch: hooks.refetchInvitations,
  }),
}))

let container: HTMLDivElement
let root: Root
let previousActEnvironment: boolean | undefined

function element(selector: string): Element {
  const match = container.querySelector(selector)
  if (!match) throw new Error(`Missing test element: ${selector}`)
  return match
}

function renderOnboarding() {
  act(() => {
    root.render(<Onboarding />)
  })
}

function setOrganizationName(name: string) {
  const input = element('#org-name') as HTMLInputElement
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
}

beforeAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  previousActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = true
})

afterAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment
})

beforeEach(() => {
  hooks.acceptInvitation.mockReset()
  hooks.createOrganization.mockReset()
  hooks.declineInvitation.mockReset()
  hooks.refetchInvitations.mockReset()
  hooks.pendingInvitations.length = 0
  hooks.createOrganization.mockImplementation(() => new Promise(() => undefined))
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
    hooks.createOrganization.mockResolvedValueOnce(undefined)
    renderOnboarding()
    setOrganizationName('Acme Inc.')

    await submitOrganization()

    expect(hooks.createOrganization).toHaveBeenCalledOnce()
    expect(window.location.pathname).toBe('/')
  })

  it('returns to the device approval page it was sent from after creating the organization', async () => {
    window.history.replaceState(
      null,
      '',
      '/onboarding?return_to=%2Fdevice%3Fuser_code%3DABCDE-F1234',
    )
    hooks.createOrganization.mockResolvedValueOnce(undefined)
    renderOnboarding()

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
    const invitation = {
      id: 'inv_1',
      org_id: 'org_1',
      org_name: 'Acme Inc.',
      email: 'person@example.com',
      org_role: 'member',
      created_at: '2026-01-01T00:00:00Z',
    }
    hooks.pendingInvitations.push(invitation)
    hooks.acceptInvitation.mockResolvedValueOnce(invitation)
    renderOnboarding()

    const accept = Array.from(container.querySelectorAll('button')).find(
      (candidate) => candidate.textContent.trim() === 'Accept',
    )
    if (!accept) throw new Error('Missing Accept button')
    await act(async () => {
      accept.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(hooks.acceptInvitation).toHaveBeenCalledWith('inv_1')
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
    hooks.createOrganization.mockResolvedValueOnce(undefined)
    renderOnboarding()
    setOrganizationName('Acme Inc.')

    await submitOrganization()

    expect(window.location.host).toBe(host)
    expect(window.location.pathname).toBe('/')
  })

  it('does not throw when a translator wraps the label before the pending update', async () => {
    renderOnboarding()
    setOrganizationName('Acme Inc.')

    const button = element('button[type="submit"]') as HTMLButtonElement
    const loadingIcon = element('[data-slot="button-loading-icon"]') as HTMLSpanElement
    const label = element('[data-slot="button-label"]') as HTMLSpanElement
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
