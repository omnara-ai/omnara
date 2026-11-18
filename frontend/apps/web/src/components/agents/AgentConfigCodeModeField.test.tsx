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

it('removes the section when turned off', () => {
  expect(toggle({ enabled: true })).toHaveBeenCalledWith(undefined)
})
