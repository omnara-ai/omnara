/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { enableReactActEnvironment } from '@/test/react-act'

import { AgentCardTime } from './AgentCardList'

let root: Root
let container: HTMLDivElement
let restore: () => void
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
})

it('shows an ongoing state in place of the time, and the time otherwise', () => {
  const value = new Date(Date.now() - 5 * 60_000).toISOString()
  act(() => {
    root.render(
      <>
        <AgentCardTime label="Last active" value={value} status="Working" />
        <AgentCardTime label="Last active" value={value} />
      </>,
    )
  })
  const times = [...container.querySelectorAll('time')]
  expect(times.map((time) => time.textContent)).toEqual(['Working', '5m ago'])
  expect(times[0]?.getAttribute('title')).toMatch(/^Last active /)
})
