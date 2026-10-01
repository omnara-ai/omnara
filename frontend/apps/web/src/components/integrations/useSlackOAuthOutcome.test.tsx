/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { enableReactActEnvironment } from '@/test/react-act'
import { button } from '@/test/secret-editor'

import { useSlackOAuthOutcome } from './useSlackOAuthOutcome'

let root: Root, container: HTMLDivElement, restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restore()
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

function Outcome() {
  const { outcome, clear } = useSlackOAuthOutcome('itg_current')
  return outcome ? (
    <>
      <p role={outcome.kind === 'error' ? 'alert' : 'status'}>
        {outcome.kind === 'error' ? outcome.description : 'Connected'}
      </p>
      <button type="button" onClick={clear}>
        Clear outcome
      </button>
    </>
  ) : null
}

it.each([
  ['integration_oauth=success&integration_id=itg_current', 'Connected'],
  ['integration_oauth=success&integration_id=itg_other', ''],
  ['integration_oauth=success', ''],
  ['integration_oauth=pending&integration_id=itg_current', ''],
  [
    'integration_oauth_error=integration_setup_changed',
    'Refresh the integration and start setup again.',
  ],
  [
    'integration_oauth_error=integration_deleted',
    'Choose or create an integration before starting setup again.',
  ],
  ['integration_oauth_error=flow_consumed', 'Refresh the integration to see its current setup.'],
  [
    'integration_oauth=success&integration_id=itg_current&integration_oauth_error=missing_scope',
    'Please approve the requested permissions and try again.',
  ],
])('consumes %s while preserving unrelated URL and history state', (query, expected) => {
  const pagePath = '/projects/project/integrations/itg_current'
  const remaining = '?filter=one&filter=two&draft=keep#profiles'
  const historyState = { key: 'router-key', index: 4 }
  window.history.replaceState(
    historyState,
    '',
    `${pagePath}?filter=one&filter=two&draft=keep&${query}#profiles`,
  )
  act(() => {
    root.render(<Outcome />)
  })
  if (expected) expect(container.textContent).toContain(expected)
  else expect(container.textContent).toBe('')
  expect(window.location.pathname + window.location.search + window.location.hash).toBe(
    pagePath + remaining,
  )
  expect(window.history.state).toEqual(historyState)
  act(() => {
    root.render(<Outcome />)
  })
  if (expected) expect(container.textContent).toContain(expected)
  act(() => {
    root.render(null)
  })
  act(() => {
    root.render(<Outcome />)
  })
  expect(container.textContent).toBe('')
})

it('does not replace history when there is no OAuth outcome', () => {
  window.history.replaceState(
    null,
    '',
    '/projects/project/integrations/itg_current?draft=keep#profiles',
  )
  const replace = vi.spyOn(window.history, 'replaceState')
  act(() => {
    root.render(<Outcome />)
  })
  expect(container.textContent).toBe('')
  expect(replace).not.toHaveBeenCalled()
})

it.each([
  'integration_oauth_error=integration_setup_changed',
  'integration_oauth=success&integration_id=itg_current',
])('clears %s without changing unrelated URL state', (query) => {
  const pagePath = '/projects/project/integrations/itg_current'
  window.history.replaceState(
    { checkpoint: 'kept' },
    '',
    `${pagePath}?draft=keep&${query}#profiles`,
  )
  act(() => {
    root.render(<Outcome />)
  })
  expect(container.querySelector('[role="alert"], [role="status"]')).not.toBeNull()
  act(() => {
    button('Clear outcome').click()
  })
  expect(container.textContent).toBe('')
  act(() => {
    root.render(<Outcome />)
  })
  expect(container.textContent).toBe('')
  expect(window.location.pathname + window.location.search + window.location.hash).toBe(
    `${pagePath}?draft=keep#profiles`,
  )
  expect(window.history.state).toEqual({ checkpoint: 'kept' })
})
