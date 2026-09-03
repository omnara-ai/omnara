/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { DeviceAuth } from '@/routes/DeviceAuth'
import {
  emptyResponse,
  type FakeApi,
  fakeApi,
  type FakeRoute,
  jsonResponse,
  neverResponds,
} from '@/test/fake-api'
import { enableReactActEnvironment } from '@/test/react-act'

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

function deviceAuthApi({
  approve = () => emptyResponse(),
  deny = neverResponds,
}: {
  approve?: FakeRoute['respond']
  deny?: FakeRoute['respond']
} = {}): FakeApi {
  const api = fakeApi([
    {
      method: 'GET',
      path: '/api/auth/device/pending',
      respond: () =>
        jsonResponse({
          client_name: 'Omnara CLI',
          token_name: 'CLI token',
          created_at: '2026-01-01T00:00:00Z',
          expires_at: '2026-01-01T00:10:00Z',
        }),
    },
    { method: 'POST', path: '/api/auth/device/approve', respond: approve },
    { method: 'POST', path: '/api/auth/device/deny', respond: deny },
  ])
  vi.stubGlobal('fetch', api.fetch)
  return api
}

async function renderDeviceAuth() {
  await act(async () => {
    root.render(<DeviceAuth />)
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
  vi.unstubAllGlobals()
})

describe('Device authorization decisions', () => {
  it('asks the user to match the code shown by the CLI', async () => {
    deviceAuthApi()
    await renderDeviceAuth()

    expect(container.textContent).toContain(
      'Only approve if this code matches the one shown by the CLI where you started login.',
    )
  })

  it('confirms approval and sends the user back to the CLI', async () => {
    const api = deviceAuthApi()
    await renderDeviceAuth()

    await act(async () => {
      button('Approve').click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    expect(
      api.requestsTo('POST', '/api/auth/device/approve').map((request) => request.body),
    ).toEqual([{ user_code: 'ABCD-EFGH' }])
    expect(container.textContent).toContain('Device approved')
    expect(container.textContent).toContain('Return to the CLI to continue.')
  })

  it('marks Deny, rather than Approve, as busy while denial is pending', async () => {
    const api = deviceAuthApi()
    await renderDeviceAuth()

    const approve = button('Approve')
    const deny = button('Deny')

    await act(async () => {
      deny.click()
      await Promise.resolve()
    })

    expect(api.requestsTo('POST', '/api/auth/device/deny').map((request) => request.body)).toEqual([
      { user_code: 'ABCD-EFGH' },
    ])
    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(true)
    expect(approve.getAttribute('aria-busy')).toBeNull()
    expect(deny.getAttribute('aria-busy')).toBe('true')
  })
})
