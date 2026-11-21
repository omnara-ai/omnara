/** @vitest-environment happy-dom */

import { QueryClient, QueryClientProvider, useInfiniteQuery } from '@tanstack/react-query'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
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

describe('multi-select search', () => {
  const searchableItems = [
    { id: 'alpha', name: 'Alpha' },
    { id: 'extract', name: 'Extract Calendar Events' },
  ]
  let selected: TestItem[] = []

  function SearchableMultiCombobox() {
    const search = useTypeaheadSearch()
    const query = useInfiniteQuery({
      queryKey: ['items', search.name ?? ''],
      queryFn: () => Promise.resolve({ data: searchableItems, next_cursor: null }),
      initialPageParam: null,
      getNextPageParam: () => undefined,
    })
    const [value, setValue] = useState<TestItem[]>([])
    return (
      <TestMultiCombobox
        items={useInfiniteQueryItems(query)}
        value={value}
        onValueChange={(items) => {
          setValue(items)
          selected = items
        }}
        search={search}
        query={query}
      />
    )
  }

  beforeEach(() => {
    selected = []
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  async function renderSearch() {
    await act(async () => {
      root.render(
        <QueryClientProvider client={new QueryClient()}>
          <SearchableMultiCombobox />
        </QueryClientProvider>,
      )
      await vi.advanceTimersByTimeAsync(0)
    })
    const input = container.querySelector<HTMLInputElement>('input[role="combobox"]')
    if (!input) throw new Error('Missing search input')
    return input
  }

  async function search(input: HTMLInputElement, text: string) {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, text)
      input.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText' }))
      await vi.advanceTimersByTimeAsync(300)
    })
    await act(() => vi.advanceTimersByTimeAsync(0))
  }

  function pressEnter(input: HTMLInputElement) {
    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    act(() => {
      input.dispatchEvent(enter)
    })
    return enter
  }

  it('selects a typed match with Enter once search results load', async () => {
    const input = await renderSearch()
    await search(input, 'Extract')

    pressEnter(input)

    expect(selected.map((candidate) => candidate.name)).toEqual(['Extract Calendar Events'])
  })

  it('keeps Enter from submitting the form only while the list is open', async () => {
    const input = await renderSearch()
    await search(input, 'zzz')
    expect(input.getAttribute('aria-expanded')).toBe('true')
    expect(pressEnter(input).defaultPrevented).toBe(true)

    act(() => {
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(input.getAttribute('aria-expanded')).not.toBe('true')
    expect(pressEnter(input).defaultPrevented).toBe(false)
  })
})
