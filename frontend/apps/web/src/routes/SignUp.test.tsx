/** @vitest-environment happy-dom */

import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { SignUp } from '@/routes/SignUp'

const sdk = vi.hoisted(() => ({ requestSignup: vi.fn() }))

vi.mock('@omnara/sdk/browser', () => sdk)
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, search }: { children: ReactNode; search?: { return_to?: string } }) => (
    <a href="/" data-return-to={search?.return_to}>
      {children}
    </a>
  ),
}))
vi.mock('@/components/auth/SocialButtons', () => ({
  SocialButtons: ({ returnTo }: { returnTo: string }) => (
    <div data-testid="social-return-to" data-return-to={returnTo} />
  ),
}))

let container: HTMLDivElement
let root: Root
let previousActEnvironment: boolean | undefined

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
  sdk.requestSignup.mockReset()
  sdk.requestSignup.mockResolvedValue(undefined)
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
})

describe('signup continuation', () => {
  it('preserves the device approval destination for social and email signup', async () => {
    act(() => {
      root.render(<SignUp />)
    })

    const returnTo = '/device?user_code=ABCDE-F1234'
    expect(
      container.querySelector('[data-testid="social-return-to"]')?.getAttribute('data-return-to'),
    ).toBe(returnTo)
    expect(
      Array.from(container.querySelectorAll('a'))
        .find((link) => link.textContent === 'Sign in')
        ?.getAttribute('data-return-to'),
    ).toBe(returnTo)

    const input = container.querySelector<HTMLInputElement>('#email')
    if (!input) throw new Error('Missing email input')
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

    expect(sdk.requestSignup).toHaveBeenCalledWith('new@example.com', returnTo)
    expect(
      Array.from(container.querySelectorAll('a'))
        .find((link) => link.textContent === 'Back to sign in')
        ?.getAttribute('data-return-to'),
    ).toBe(returnTo)
  })
})
