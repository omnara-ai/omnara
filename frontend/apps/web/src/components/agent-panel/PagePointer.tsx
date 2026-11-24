import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'

import type { PagePointer as Pointer } from './page-actions'

const pad = 6
const lifetimeMs = 30_000

/** Marks the element the assistant is pointing at, with its note, until the user moves on. */
export function PagePointer({
  pointer,
  onDismiss,
}: {
  pointer: Pointer | null
  onDismiss: () => void
}) {
  if (pointer == null) return null
  return createPortal(<Mark pointer={pointer} onDismiss={onDismiss} />, document.body)
}

function Mark({ pointer, onDismiss }: { pointer: Pointer; onDismiss: () => void }) {
  const frame = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const { element } = pointer
    const rect = element.getBoundingClientRect()
    if (rect.top < 0 || rect.bottom > window.innerHeight) {
      element.scrollIntoView({ block: 'center', behavior: 'smooth' })
    }
    let next = 0
    function follow() {
      const node = frame.current
      if (!element.isConnected) {
        onDismiss()
        return
      }
      if (node != null) {
        const box = element.getBoundingClientRect()
        node.style.transform = `translate(${String(box.left - pad)}px, ${String(box.top - pad)}px)`
        node.style.width = `${String(box.width + pad * 2)}px`
        node.style.height = `${String(box.height + pad * 2)}px`
        node.dataset.below = String(box.top < 56)
        node.dataset.end = String(box.left > window.innerWidth / 2)
      }
      next = requestAnimationFrame(follow)
    }
    follow()
    const timer = setTimeout(onDismiss, lifetimeMs)
    const listeners = new AbortController()
    window.addEventListener('pointerdown', onDismiss, { capture: true, signal: listeners.signal })
    window.addEventListener(
      'keydown',
      (event) => {
        if (event.key === 'Escape') onDismiss()
      },
      { signal: listeners.signal },
    )
    return () => {
      cancelAnimationFrame(next)
      clearTimeout(timer)
      listeners.abort()
    }
  }, [pointer, onDismiss])

  return (
    <div
      ref={frame}
      data-assistant-surface
      className="group/pointer ring-primary bg-primary/10 animate-in fade-in-0 pointer-events-none fixed top-0 left-0 z-[60] rounded-lg ring-2 duration-200 motion-reduce:animate-none"
    >
      {pointer.note != null && pointer.note !== '' && (
        <p
          role="status"
          className="bg-primary text-primary-foreground absolute bottom-full left-0 mb-2 w-max max-w-64 rounded-md px-2.5 py-1.5 text-xs font-medium shadow-md group-data-[below=true]/pointer:top-full group-data-[below=true]/pointer:bottom-auto group-data-[below=true]/pointer:mt-2 group-data-[below=true]/pointer:mb-0 group-data-[end=true]/pointer:right-0 group-data-[end=true]/pointer:left-auto"
        >
          {pointer.note}
        </p>
      )}
    </div>
  )
}
