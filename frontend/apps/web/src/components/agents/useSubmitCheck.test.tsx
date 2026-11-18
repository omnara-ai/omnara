/** @vitest-environment happy-dom */

import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it } from 'vitest'

import { useSubmitCheck } from '@/components/agents/useSubmitCheck'
import { enableReactActEnvironment } from '@/test/react-act'

function Harness({ problems, unchanged }: { problems: string[]; unchanged: boolean }) {
  const submitCheck = useSubmitCheck(problems, unchanged)
  const [allowed, setAllowed] = useState<boolean | null>(null)
  return (
    <>
      <button
        type="button"
        onClick={() => {
          setAllowed(submitCheck.check())
        }}
      >
        Save
      </button>
      <output
        data-allowed={String(allowed)}
        data-problems={JSON.stringify(submitCheck.problems)}
        data-unchanged={String(submitCheck.unchanged)}
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
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
})

function render(problems: string[], unchanged = false) {
  act(() => {
    root.render(<Harness problems={problems} unchanged={unchanged} />)
  })
}

function clickSave() {
  act(() => {
    container.querySelector('button')?.click()
  })
}

function state() {
  const output = container.querySelector('output')
  const problems: unknown = JSON.parse(output?.getAttribute('data-problems') ?? '[]')
  return {
    allowed: output?.getAttribute('data-allowed'),
    problems,
    unchanged: output?.getAttribute('data-unchanged'),
  }
}

it('shows problems instead of no changes when the edit serializes to the saved config', () => {
  render(['Subagent names must be unique: “helper”.'], true)
  expect(state()).toMatchObject({ problems: [], unchanged: 'false' })

  clickSave()
  expect(state()).toEqual({
    allowed: 'false',
    problems: ['Subagent names must be unique: “helper”.'],
    unchanged: 'false',
  })
})

it('reports no changes until something changes', () => {
  render([], true)
  clickSave()
  expect(state()).toEqual({ allowed: 'false', problems: [], unchanged: 'true' })

  render(['Instructions are required.'])
  expect(state()).toMatchObject({ problems: [], unchanged: 'false' })
})

it('keeps shown problems current and clears them once fixed', () => {
  render(['Choose a model.', 'Instructions are required.'])
  clickSave()

  render(['Instructions are required.'])
  expect(state().problems).toEqual(['Instructions are required.'])

  render([])
  expect(state().problems).toEqual([])
  clickSave()
  expect(state().allowed).toBe('true')
})
