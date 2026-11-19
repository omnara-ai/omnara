/** @vitest-environment happy-dom */

import type { AgentConfigDefinitionCodeMode } from '@omnara/sdk'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentConfigCodeModeField } from '@/components/agents/AgentConfigCodeModeField'
import { enableReactActEnvironment } from '@/test/react-act'

let container: HTMLDivElement
let root: Root

beforeAll(enableReactActEnvironment)
beforeEach(() => {
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

function toggle(value: AgentConfigDefinitionCodeMode | undefined) {
  const onChange = vi.fn()
  act(() => {
    root.render(<AgentConfigCodeModeField value={value} onChange={onChange} />)
  })
  const checkbox = container.querySelector<HTMLInputElement>('input[type="checkbox"]')
  act(() => {
    checkbox?.click()
  })
  return onChange
}

it('sets code mode when turned on', () => {
  expect(toggle(undefined)).toHaveBeenCalledWith({ enabled: true })
  expect(toggle({ enabled: false })).toHaveBeenCalledWith({ enabled: true })
})

it('keeps the default when turned on', () => {
  expect(toggle({ enabled: false, default_allowed: false })).toHaveBeenCalledWith({
    enabled: true,
    default_allowed: false,
  })
})

it('removes the section when turned off', () => {
  expect(toggle({ enabled: true })).toHaveBeenCalledWith(undefined)
})

it('keeps the default through an off and on round trip', () => {
  const disabled = { enabled: false, default_allowed: false }
  expect(toggle({ enabled: true, default_allowed: false })).toHaveBeenCalledWith(disabled)
  expect(toggle(disabled)).toHaveBeenCalledWith({ enabled: true, default_allowed: false })
})

it('warns that agents sharing a machine can call each other’s tools', () => {
  toggle(undefined)
  expect(container.textContent).toContain('Agents on the same machine can call each other’s tools')
})
