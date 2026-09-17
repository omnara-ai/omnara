/** @vitest-environment happy-dom */
import { afterEach, describe, expect, it } from 'vitest'

import {
  installImeCompositionTracker,
  isImeComposing,
  SAFARI_IME_RACE_WINDOW_MS,
} from '@/lib/ime-composition'

function enter(isComposing: boolean) {
  const event = new KeyboardEvent('keydown', { key: 'Enter' })
  Object.defineProperty(event, 'isComposing', { value: isComposing })
  return event
}

describe('isImeComposing', () => {
  let uninstall: () => void

  afterEach(() => {
    uninstall()
  })

  it('reports a composition the event itself admits to', () => {
    uninstall = installImeCompositionTracker(window)

    expect(isImeComposing(enter(true))).toBe(true)
  })

  it('reports a composition still open, whatever the event says', () => {
    uninstall = installImeCompositionTracker(window)
    window.dispatchEvent(new CompositionEvent('compositionstart'))

    expect(isImeComposing(enter(false))).toBe(true)
  })

  it('reports the Enter that confirmed a candidate on Safari', () => {
    // Safari's ordering: compositionend lands first, so the confirming keydown
    // claims no composition is in progress.
    uninstall = installImeCompositionTracker(window)
    window.dispatchEvent(new CompositionEvent('compositionstart'))
    window.dispatchEvent(new CompositionEvent('compositionend'))

    expect(isImeComposing(enter(false))).toBe(true)
  })

  it('lets a deliberate second Enter through', () => {
    uninstall = installImeCompositionTracker(window)
    window.dispatchEvent(new CompositionEvent('compositionstart'))
    window.dispatchEvent(new CompositionEvent('compositionend'))

    const wellAfter = performance.now() + SAFARI_IME_RACE_WINDOW_MS + 1
    expect(isImeComposing(enter(false), wellAfter)).toBe(false)
  })

  it('sees a composition an editor keeps to itself', () => {
    // Capture runs before the target's own handlers, so a rich editor that
    // stops propagation cannot hide the composition from the tracker.
    uninstall = installImeCompositionTracker(window)
    const editor = document.createElement('div')
    document.body.appendChild(editor)
    editor.addEventListener('compositionstart', (event) => {
      event.stopPropagation()
    })

    editor.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))

    expect(isImeComposing(enter(false))).toBe(true)
    editor.remove()
  })

  it('lets Enter through when no composition ever started', () => {
    uninstall = installImeCompositionTracker(window)

    expect(isImeComposing(enter(false))).toBe(false)
  })

  it('forgets an abandoned composition when the field loses focus', () => {
    uninstall = installImeCompositionTracker(window)
    window.dispatchEvent(new CompositionEvent('compositionstart'))
    window.dispatchEvent(new FocusEvent('blur'))

    const wellAfter = performance.now() + SAFARI_IME_RACE_WINDOW_MS + 1
    expect(isImeComposing(enter(false), wellAfter)).toBe(false)
  })
})
