/** @vitest-environment happy-dom */

import { act, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { containFileDrop, installFileDropGuard } from '@/lib/file-drop-guard'
import { enableReactActEnvironment } from '@/test/react-act'

import { useFileDrop } from './useFileDrop'

it('isolates two composers, clears scoped drags, and preserves accepted drops with the global guard', () => {
  const restore = enableReactActEnvironment()
  const uninstall = installFileDropGuard(window)
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  const mainDrop = vi.fn()
  const panelDrop = vi.fn()
  function Targets() {
    const scope = useRef<HTMLDivElement>(null)
    const mainDragging = useFileDrop(true, mainDrop)
    const panelDragging = useFileDrop(true, panelDrop, scope)
    return (
      <>
        <div id="main-drop" data-dragging={mainDragging} />
        <div onDragOver={containFileDrop} onDrop={containFileDrop}>
          <div id="panel-header" />
          <div id="panel-drop" ref={scope} data-dragging={panelDragging} />
        </div>
      </>
    )
  }
  function dispatch(id: string, type: string, relatedTarget?: Element | null) {
    const target = container.querySelector(`#${id}`)
    if (target == null) throw new Error(`Missing ${id}`)
    const transfer = {
      types: ['Files'],
      files: [new File(['hello'], 'note.txt')],
      dropEffect: 'none',
    }
    const event = new Event(type, { bubbles: true, cancelable: true })
    Object.defineProperties(event, {
      dataTransfer: { value: transfer },
      relatedTarget: { value: relatedTarget },
    })
    act(() => {
      target.dispatchEvent(event)
    })
    return transfer
  }
  try {
    act(() => {
      root.render(<Targets />)
    })
    expect(dispatch('main-drop', 'dragover').dropEffect).toBe('copy')
    dispatch('main-drop', 'drop')
    expect(mainDrop).toHaveBeenCalledTimes(1)
    expect(dispatch('panel-drop', 'dragover').dropEffect).toBe('copy')
    expect(container.querySelector('#panel-drop')?.getAttribute('data-dragging')).toBe('true')
    dispatch('panel-drop', 'dragleave', container.querySelector('#panel-header'))
    expect(container.querySelector('#panel-drop')?.getAttribute('data-dragging')).toBe('false')
    dispatch('panel-drop', 'dragover')
    dispatch('panel-drop', 'drop')
    expect(panelDrop).toHaveBeenCalledTimes(1)
    expect(mainDrop).toHaveBeenCalledTimes(1)
    dispatch('panel-header', 'drop')
    expect(mainDrop).toHaveBeenCalledTimes(1)
    expect(panelDrop).toHaveBeenCalledTimes(1)
    expect(container.querySelector('#panel-drop')?.getAttribute('data-dragging')).toBe('false')
  } finally {
    act(() => {
      root.unmount()
    })
    container.remove()
    uninstall()
    restore()
  }
})
