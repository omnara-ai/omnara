import * as z from 'zod'

import { readLocal, writeLocal } from '@/lib/storage'

export const cursorID = 'assistant-cursor'

// Where the cursor last clicked, so its next move starts there instead of at the chat box.
const positionKey = 'omnara:assistant-cursor'
const positionSchema = z.object({ x: z.number(), y: z.number() })

const glideMs = 700
const restMs = 150
const pressMs = 260
const lingerMs = 1500
const fadeMs = 300

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

function inViewport(element: Element): boolean {
  const rect = element.getBoundingClientRect()
  return (
    rect.top >= 0 &&
    rect.left >= 0 &&
    rect.bottom <= window.innerHeight &&
    rect.right <= window.innerWidth
  )
}

function lastPosition(): { x: number; y: number } | null {
  try {
    const stored = positionSchema.safeParse(JSON.parse(readLocal(positionKey) ?? 'null'))
    if (!stored.success) return null
    // The window may have been resized since.
    return {
      x: Math.min(Math.max(stored.data.x, 0), window.innerWidth - 32),
      y: Math.min(Math.max(stored.data.y, 0), window.innerHeight - 32),
    }
  } catch {
    return null
  }
}

let fading: ReturnType<typeof setTimeout> | undefined

function place(cursor: HTMLElement, x: number, y: number, ms: number) {
  cursor.style.transitionDuration = `${String(ms)}ms`
  cursor.style.transform = `translate(${String(x)}px, ${String(y)}px)`
}

/**
 * Shows the assistant's cursor travelling to the element and clicking it. It starts from
 * where it last clicked, or from the conversation the first time. With reduced motion the
 * cursor appears on the element and clicks without travelling. The caller does what the
 * click stands for.
 */
export async function clickWithCursor(target: Element): Promise<void> {
  const cursor = document.getElementById(cursorID)
  if (cursor == null) return
  clearTimeout(fading)
  if (!inViewport(target)) target.scrollIntoView({ block: 'center' })
  const to = target.getBoundingClientRect()
  const x = to.left + Math.min(to.width / 2, 40)
  const y = to.top + to.height / 2
  const travels = !window.matchMedia('(prefers-reduced-motion: reduce)').matches
  if (travels) {
    const composer = document.querySelector('[data-assistant-surface] textarea')
    const chat = composer?.checkVisibility() === true ? composer.getBoundingClientRect() : null
    const from = lastPosition() ?? {
      x: chat?.left ?? window.innerWidth - 48,
      y: chat?.top ?? window.innerHeight - 48,
    }
    place(cursor, from.x, from.y, 0)
  } else {
    place(cursor, x, y, 0)
  }
  cursor.dataset.state = 'moving'
  if (travels) {
    // Reading layout applies the starting position before the glide begins.
    cursor.getBoundingClientRect()
    place(cursor, x, y, glideMs)
    await wait(glideMs)
  }
  writeLocal(positionKey, JSON.stringify({ x, y }))
  await wait(restMs)
  cursor.dataset.state = 'pressing'
  await wait(pressMs)
  cursor.dataset.state = 'moving'
}

/** Lets the cursor fade once it has rested a moment; another click keeps it on screen. */
export function releaseCursor() {
  const cursor = document.getElementById(cursorID)
  if (cursor == null) return
  clearTimeout(fading)
  fading = setTimeout(() => {
    cursor.style.transitionDuration = `${String(fadeMs)}ms`
    cursor.dataset.state = 'hidden'
  }, lingerMs)
}

/**
 * Clicks the element the way a mouse does. Menus, tabs and selects act on the press rather
 * than on the click that follows it, so the whole sequence is sent.
 */
export function press(element: Element) {
  const rect = element.getBoundingClientRect()
  const at = {
    bubbles: true,
    cancelable: true,
    composed: true,
    view: window,
    button: 0,
    clientX: rect.left + rect.width / 2,
    clientY: rect.top + rect.height / 2,
  }
  const pointer = { ...at, pointerType: 'mouse', isPrimary: true }
  element.dispatchEvent(new PointerEvent('pointerdown', { ...pointer, buttons: 1 }))
  element.dispatchEvent(new MouseEvent('mousedown', { ...at, buttons: 1 }))
  element.dispatchEvent(new PointerEvent('pointerup', pointer))
  element.dispatchEvent(new MouseEvent('mouseup', at))
  if (element instanceof HTMLElement) element.click()
  else element.dispatchEvent(new MouseEvent('click', at))
}
