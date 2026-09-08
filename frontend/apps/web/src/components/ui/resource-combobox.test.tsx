/** @vitest-environment happy-dom */

import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { enableReactActEnvironment } from '@/test/react-act'

const firstModel = { id: 'first', label: 'First model · provider' }
const secondModel = { id: 'second', label: 'Second model · provider' }
const models = [firstModel, secondModel]
const ModelPicker = createResourceCombobox<(typeof models)[number]>({
  itemKey: (model) => model.id,
  itemLabel: (model) => model.label,
  placeholder: 'Search models…',
})

function Harness() {
  const [value, setValue] = useState<(typeof models)[number] | null>(firstModel)
  const [search, setSearch] = useState('')
  return (
    <>
      <label htmlFor="model">Model</label>
      <ModelPicker
        id="model"
        required
        items={models}
        value={value}
        onValueChange={setValue}
        search={{ search, setSearch }}
      />
    </>
  )
}

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
afterEach(async () => {
  await interact(() => {
    root.unmount()
  })
  container.remove()
})

async function interact(action: () => void) {
  await act(async () => {
    action()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function trigger() {
  const button = container.querySelector<HTMLButtonElement>('button[role="combobox"]')
  if (!button) throw new Error('Missing picker trigger')
  return button
}

function searchInput() {
  const input = document.querySelector<HTMLInputElement>('input[placeholder="Search models…"]')
  if (!input) throw new Error('Missing search input')
  return input
}

async function typeSearch(value: string) {
  await interact(() => {
    const input = searchInput()
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText' }))
  })
}

it('keeps the selection separate from an editable, clearable search', async () => {
  await interact(() => {
    root.render(<Harness />)
  })
  expect(trigger().textContent).toBe(firstModel.label)
  expect(trigger().getAttribute('aria-required')).toBe('true')

  await interact(() => {
    trigger().click()
  })
  expect(searchInput().value).toBe('')
  await typeSearch('Second')
  expect(searchInput().value).toBe('Second')
  expect(trigger().textContent).toBe(firstModel.label)
  await typeSearch('')
  expect(searchInput().value).toBe('')
  expect(trigger().textContent).toBe(firstModel.label)

  await typeSearch('Second')
  await interact(() => {
    const option = document.querySelector<HTMLElement>('[role="option"]')
    expect(option?.textContent).toBe(secondModel.label)
    option?.click()
  })
  expect(trigger().textContent).toBe(secondModel.label)
  await vi.waitFor(() => {
    expect(trigger().getAttribute('aria-expanded')).toBe('false')
  })
  await interact(() => {
    trigger().click()
  })
  expect(searchInput().value).toBe('')
})

it('clears the actual selection and returns keyboard focus to the picker', async () => {
  await interact(() => {
    root.render(<Harness />)
  })
  await interact(() => {
    container.querySelector<HTMLButtonElement>('button[aria-label^="Clear "]')?.click()
  })
  expect(trigger().textContent).toBe('Search models…')
  expect(document.activeElement).toBe(trigger())
  expect(container.querySelector('button[aria-label^="Clear "]')).toBeNull()
  await interact(() => {
    trigger().click()
  })
  expect(searchInput().value).toBe('')
  expect(trigger().textContent).toBe('Search models…')
})
