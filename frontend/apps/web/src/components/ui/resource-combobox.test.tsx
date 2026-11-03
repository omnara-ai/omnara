/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it } from 'vitest'

import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

interface TestItem {
  id: string
  name: string
}

const TestCombobox = createResourceCombobox<TestItem>({
  itemKey: (item) => item.id,
  itemLabel: (item) => item.name,
  placeholder: 'Search items…',
})
const TestMultiCombobox = createResourceMultiCombobox<TestItem>({
  itemKey: (item) => item.id,
  itemLabel: (item) => item.name,
  placeholder: 'Select items…',
})
const item = { id: 'item-1', name: 'gpt-5.6' }

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
})

function renderCombobox(value: TestItem | null) {
  act(() => {
    root.render(
      <TestCombobox
        items={[item]}
        value={value}
        placeholder="Loading items…"
        onValueChange={() => undefined}
      />,
    )
  })
}

it('shows the selected label when a translator rewrites the previous label late', () => {
  renderCombobox(null)
  const trigger = container.querySelector('[role="combobox"]')
  const loadingLabel = trigger?.querySelector('span')?.firstChild
  if (!trigger || !loadingLabel) throw new Error('Missing trigger label')

  renderCombobox(item)
  const translated = document.createElement('span')
  translated.textContent = '正在加载…'
  loadingLabel.parentNode?.replaceChild(translated, loadingLabel)

  expect(trigger.textContent).toBe('gpt-5.6')
})

it('keeps multi-select menus open after selection by default', async () => {
  let selected: TestItem[] = []
  act(() => {
    root.render(
      <TestMultiCombobox
        items={[item]}
        value={[]}
        onValueChange={(value) => {
          selected = value
        }}
      />,
    )
  })
  const input = container.querySelector('[role="combobox"]')
  act(() => {
    input?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="option"]')).not.toBeNull()
  })
  act(() => {
    document.querySelector<HTMLElement>('[role="option"]')?.click()
  })
  expect(selected).toEqual([item])
  expect(input?.getAttribute('aria-expanded')).toBe('true')
})
