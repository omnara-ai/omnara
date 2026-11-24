/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { AssistantContext } from '@/components/agent-panel/assistant-context'
import { currentUserOrg } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

import { AssistantHome, AssistantInvite } from './OverviewAssistant'

it('gives the conversation the main column, and invites setup through the corner panel', () => {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const open = vi.fn()
  const setDock = vi.fn()
  function render(view: 'home' | 'invite' | 'none') {
    act(() => {
      root.render(
        <AssistantContext
          value={{
            bootstrap: null,
            open,
            ask: vi.fn(),
            registerSender: vi.fn(),
            setDock,
            hideLauncher: vi.fn(),
            point: vi.fn(),
            follow: vi.fn(),
          }}
        >
          {view === 'home' && <AssistantHome />}
          {view === 'invite' && <AssistantInvite org={currentUserOrg()} />}
        </AssistantContext>,
      )
    })
  }
  try {
    render('home')
    const column = container.querySelector('section[aria-label="Assistant"]')
    expect(column).not.toBeNull()
    expect(setDock.mock.calls.at(-1)?.[0]).toBe(column)

    render('none')
    expect(setDock.mock.calls.at(-1)?.[0]).toBeNull()

    render('invite')
    act(() => {
      container.querySelector('button')?.click()
    })
    expect(open).toHaveBeenCalledOnce()
  } finally {
    act(() => {
      root.unmount()
    })
    container.remove()
    restore()
  }
})
