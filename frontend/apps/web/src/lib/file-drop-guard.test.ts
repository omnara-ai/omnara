/** @vitest-environment happy-dom */
import { afterEach, describe, expect, it } from 'vitest'

import { installFileDropGuard } from '@/lib/file-drop-guard'

function dragEvent(type: string, types: string[]) {
  const event = new Event(type, { cancelable: true })
  Object.defineProperty(event, 'dataTransfer', { value: { types, dropEffect: 'copy' } })
  return event
}

describe('installFileDropGuard', () => {
  let uninstall: () => void

  afterEach(() => {
    uninstall()
  })

  it('stops the browser from navigating to a dropped file', () => {
    uninstall = installFileDropGuard(window)
    const dragover = dragEvent('dragover', ['Files'])
    const drop = dragEvent('drop', ['Files'])

    window.dispatchEvent(dragover)
    window.dispatchEvent(drop)

    expect(dragover.defaultPrevented).toBe(true)
    expect(drop.defaultPrevented).toBe(true)
  })

  it('leaves non-file drags alone', () => {
    uninstall = installFileDropGuard(window)
    const drop = dragEvent('drop', ['text/plain'])

    window.dispatchEvent(drop)

    expect(drop.defaultPrevented).toBe(false)
  })
})
