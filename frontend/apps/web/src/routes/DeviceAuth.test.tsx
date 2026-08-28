/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { DeviceAuth } from '@/routes/DeviceAuth'

const sdk = vi.hoisted(() => ({
  approveDeviceAuth: vi.fn(),
  denyDeviceAuth: vi.fn(),
  pendingDeviceAuth: vi.fn(),
}))

vi.mock('@omnara/sdk/browser', () => sdk)

let container: HTMLDivElement
let root: Root
let previousActEnvironment: boolean | undefined

async function renderDeviceAuth() {
  await act(async () => {
    root.render(<DeviceAuth />)
    await Promise.resolve()
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
  sdk.approveDeviceAuth.mockReset()
  sdk.denyDeviceAuth.mockReset()
  sdk.pendingDeviceAuth.mockReset()
  sdk.pendingDeviceAuth.mockResolvedValue({ clientName: 'Omnara CLI', tokenName: 'CLI token' })
  sdk.denyDeviceAuth.mockImplementation(() => new Promise(() => undefined))
  window.history.replaceState(null, '', '/device?user_code=ABCD-EFGH')

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

describe('Device authorization decisions', () => {
  it('asks the user to match the code shown by the CLI', async () => {
    await renderDeviceAuth()

    expect(container.textContent).toContain(
      'Only approve if this code matches the one shown by the CLI where you started login.',
    )
  })

  it('redirects to the overview after approving', async () => {
    sdk.approveDeviceAuth.mockResolvedValue(undefined)
    await renderDeviceAuth()

    await act(async () => {
      button('Approve').click()
      await Promise.resolve()
    })

    expect(sdk.approveDeviceAuth).toHaveBeenCalledWith('ABCD-EFGH')
    expect(window.location.pathname).toBe('/')
  })

  it('marks Deny, rather than Approve, as busy while denial is pending', async () => {
    await renderDeviceAuth()

    const approve = button('Approve')
    const deny = button('Deny')

    await act(async () => {
      deny.click()
      await Promise.resolve()
    })

    expect(sdk.denyDeviceAuth).toHaveBeenCalledWith('ABCD-EFGH')
    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(true)
    expect(approve.getAttribute('aria-busy')).toBeNull()
    expect(deny.getAttribute('aria-busy')).toBe('true')
  })
})
