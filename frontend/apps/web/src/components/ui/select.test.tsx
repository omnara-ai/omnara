/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { enableReactActEnvironment } from '@/test/react-act'

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
  container.remove()
})

describe('SelectValue', () => {
  it('survives translator-style wrapping when the select unmounts', () => {
    act(() => {
      root.render(
        <Select value="-modified_at">
          <SelectTrigger>
            <SelectValue>Modified: Newest</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="-modified_at">Modified: Newest</SelectItem>
          </SelectContent>
        </Select>,
      )
    })

    const label = container.querySelector('[data-slot="select-value-label"]')
    const originalLabel = label?.firstChild
    if (!label || !originalLabel) throw new Error('Missing selected value label')

    const translatedLabel = document.createElement('span')
    translatedLabel.lang = 'es'
    translatedLabel.textContent = 'Modificado: más reciente'
    label.replaceChild(translatedLabel, originalLabel)

    expect(() => {
      act(() => {
        root.unmount()
      })
    }).not.toThrow()
  })
})
