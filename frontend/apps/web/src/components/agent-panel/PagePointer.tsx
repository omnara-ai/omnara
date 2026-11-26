import { useEffect, useEffectEvent, useRef } from 'react'
import { createPortal } from 'react-dom'

import type { PagePointer as Pointer } from './page-actions'

const pad = 6
const lifetimeMs = 30_000
const fadeMs = 200

/** Marks the element the assistant is pointing at, with its note, until the user moves on. It fades in and out in place. */
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

/**
 * Keeps the mark on the element: fades it in where the element is, follows the element as the
 * page moves, and fades it out when the user moves on. Returns how to stop.
 */
function showMark(node: HTMLElement, element: Element, dismiss: () => void): () => void {
  const rect = element.getBoundingClientRect()
  if (rect.top < 0 || rect.bottom > window.innerHeight) {
    element.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }
  // A mark that was showing something else takes its new place unseen, then fades in.
  node.style.transitionDuration = '0ms'
  node.dataset.state = 'hidden'
  let next = 0
  let leaving: ReturnType<typeof setTimeout> | undefined
  function leave() {
    if (leaving != null) return
    node.dataset.state = 'hidden'
    leaving = setTimeout(dismiss, fadeMs)
  }
  function follow() {
    if (!element.isConnected) {
      leave()
      return
    }
    const box = element.getBoundingClientRect()
    node.style.transform = `translate(${String(box.left - pad)}px, ${String(box.top - pad)}px)`
    node.style.width = `${String(box.width + pad * 2)}px`
    node.style.height = `${String(box.height + pad * 2)}px`
    node.dataset.below = String(box.top < 56)
    node.dataset.end = String(box.left > window.innerWidth / 2)
    next = requestAnimationFrame(follow)
  }
  follow()
  const appear = requestAnimationFrame(() => {
    node.style.transitionDuration = ''
    node.dataset.state = 'shown'
  })
  const timer = setTimeout(leave, lifetimeMs)
  const listeners = new AbortController()
  window.addEventListener('pointerdown', leave, { capture: true, signal: listeners.signal })
  window.addEventListener(
    'keydown',
    (event) => {
      if (event.key === 'Escape') leave()
    },
    { signal: listeners.signal },
  )
  return () => {
    cancelAnimationFrame(next)
    cancelAnimationFrame(appear)
    clearTimeout(timer)
    clearTimeout(leaving)
    listeners.abort()
  }
}

function Mark({ pointer, onDismiss }: { pointer: Pointer; onDismiss: () => void }) {
  const frame = useRef<HTMLDivElement>(null)
  const dismiss = useEffectEvent(onDismiss)

  useEffect(() => {
    const node = frame.current
    return node == null
      ? undefined
      : showMark(node, pointer.element, () => {
          dismiss()
        })
  }, [pointer])

  return (
    <div
      ref={frame}
      data-assistant-surface
      data-state="hidden"
      className="group/pointer ring-primary bg-primary/10 pointer-events-none fixed top-0 left-0 z-[60] rounded-lg opacity-0 ring-2 transition-opacity duration-200 data-[state=shown]:opacity-100 motion-reduce:transition-none"
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
