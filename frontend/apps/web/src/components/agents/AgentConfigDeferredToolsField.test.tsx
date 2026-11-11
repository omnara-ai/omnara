/** @vitest-environment happy-dom */

import type { AgentConfigDefinitionDeferredTools } from '@omnara/sdk'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentConfigDeferredToolsField } from '@/components/agents/AgentConfigDeferredToolsField'
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

function toggle(value: AgentConfigDefinitionDeferredTools | undefined) {
  const onChange = vi.fn()
  act(() => {
    root.render(<AgentConfigDeferredToolsField value={value} onChange={onChange} />)
  })
  const checkbox = container.querySelector<HTMLInputElement>('input[type="checkbox"]')
  act(() => {
    checkbox?.click()
  })
  return onChange
}

it('sets code mode when turned on', () => {
  expect(toggle(undefined)).toHaveBeenCalledWith({ mode: 'code_mode' })
  expect(toggle({ mode: 'tool_add' })).toHaveBeenCalledWith({ mode: 'code_mode' })
})

it('removes the section when turned off', () => {
  expect(toggle({ mode: 'code_mode' })).toHaveBeenCalledWith(undefined)
})
