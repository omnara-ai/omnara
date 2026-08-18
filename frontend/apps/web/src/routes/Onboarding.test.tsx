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
}))

vi.mock('@omnara/react', () => ({
  useAcceptInvitation: () => ({ mutateAsync: hooks.acceptInvitation }),
  useCreateOrganization: () => ({ mutateAsync: hooks.createOrganization }),
  useDeclineInvitation: () => ({ mutateAsync: hooks.declineInvitation }),
  usePendingInvitations: () => ({
    data: { data: [] },
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
