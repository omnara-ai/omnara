/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { OverflowTooltip } from '@/components/ui/overflow-tooltip'
import { enableReactActEnvironment } from '@/test/react-act'
import { translateTextNodes } from '@/test/translate-text-nodes'

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})

afterAll(() => {
  restoreActEnvironment()
})

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
  vi.restoreAllMocks()
})

it('closes after a browser translator rewrites the tooltip text', () => {
  const computedStyle = window.getComputedStyle.bind(window)
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element, pseudoElement) => {
    const style = computedStyle(element, pseudoElement)
    if (element instanceof HTMLElement && element.dataset.slot === 'tooltip-content') {
      Object.defineProperty(style, 'animationName', {
        configurable: true,
        get: () => `tooltip-${element.dataset.state ?? 'open'}`,
      })
    }
    return style
  })
  act(() => {
    root.render(
      <OverflowTooltip>
        <button type="button" style={{ overflowX: 'hidden' }}>
          openai/gpt-5.6-sol · omnara-openrouter
        </button>
      </OverflowTooltip>,
    )
  })
  const trigger = container.querySelector('button')
  if (!trigger) throw new Error('Missing tooltip trigger')
  Object.defineProperty(trigger, 'scrollWidth', { value: 200 })
  Object.defineProperty(trigger, 'clientWidth', { value: 100 })

  act(() => {
    trigger.focus()
  })
  const content = document.querySelector('[data-slot="tooltip-content"]')
  if (!content?.textContent.includes('omnara-openrouter')) throw new Error('Missing tooltip text')
  translateTextNodes(content)

  expect(() => {
    act(() => {
      trigger.blur()
    })
  }).not.toThrow()
  expect(document.querySelector('[data-slot="tooltip-content"]')).not.toBeNull()
})
