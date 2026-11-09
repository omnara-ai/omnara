/** @vitest-environment happy-dom */

import type { AgentConfigDefinitionProgrammaticToolCalls } from '@omnara/sdk'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentConfigProgrammaticToolCallsField } from '@/components/agents/AgentConfigProgrammaticToolCallsField'
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

function toggle(value: AgentConfigDefinitionProgrammaticToolCalls | undefined) {
  const onChange = vi.fn()
  act(() => {
    root.render(<AgentConfigProgrammaticToolCallsField value={value} onChange={onChange} />)
  })
  const checkbox = container.querySelector<HTMLInputElement>('input[type="checkbox"]')
  act(() => {
    checkbox?.click()
  })
  return onChange
}

it('keeps the excluded tools when programmatic tool calls are turned off and on again', () => {
  const exclude = ['web_search', 'mcp__github__create_issue']
  expect(toggle({ enabled: true, exclude })).toHaveBeenCalledWith({ enabled: false, exclude })
  expect(toggle({ enabled: false, exclude })).toHaveBeenCalledWith({ enabled: true, exclude })
})

it('removes the section when it is turned off with nothing excluded', () => {
  expect(toggle({ enabled: true })).toHaveBeenCalledWith(undefined)
})
