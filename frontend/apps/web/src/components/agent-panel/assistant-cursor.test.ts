/** @vitest-environment happy-dom */

import { expect, it } from 'vitest'

import { press } from './assistant-cursor'

it('presses an element the way a mouse does, so controls that act on the press respond', () => {
  const button = document.createElement('button')
  document.body.append(button)
  const seen: string[] = []
  for (const type of ['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click']) {
    button.addEventListener(type, (event) => {
      seen.push(`${event.type}:${String(event instanceof MouseEvent && event.button)}`)
    })
  }
  press(button)
  expect(seen).toEqual(['pointerdown:0', 'mousedown:0', 'pointerup:0', 'mouseup:0', 'click:0'])
  button.remove()
})
