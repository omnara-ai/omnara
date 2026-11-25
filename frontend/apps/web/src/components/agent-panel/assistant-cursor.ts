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

/** How long the cursor takes to reach something and press it. */
export const cursorClickMs = glideMs + restMs + pressMs

/** Lets the time pass, and stops with the signal's reason if it aborts first. */
async function wait(ms: number, signal: AbortSignal): Promise<void> {
  await new Promise<void>((resolve) => {
    const timer = setTimeout(done, ms)
    function done() {
      clearTimeout(timer)
      signal.removeEventListener('abort', done)
      resolve()
    }
    signal.addEventListener('abort', done, { once: true })
    if (signal.aborted) done()
  })
  signal.throwIfAborted()
}

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

// Room the label needs to the right of the cursor before it would leave the window.
const labelRoom = 120

function place(cursor: HTMLElement, x: number, y: number, ms: number) {
  cursor.dataset.label = x > window.innerWidth - labelRoom ? 'before' : 'after'
  cursor.style.transitionDuration = `${String(ms)}ms`
  cursor.style.transform = `translate(${String(x)}px, ${String(y)}px)`
}

/**
 * Brings the cursor to a point on screen. It starts from where it last stopped, or from the
 * conversation the first time. With reduced motion it appears there without travelling.
 */
async function travel(
  cursor: HTMLElement,
  x: number,
  y: number,
  signal: AbortSignal,
): Promise<void> {
  signal.throwIfAborted()
  clearTimeout(fading)
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
    await wait(glideMs, signal)
  }
  writeLocal(positionKey, JSON.stringify({ x, y }))
}

function fade(cursor: HTMLElement) {
  cursor.style.transitionDuration = `${String(fadeMs)}ms`
  cursor.dataset.state = 'hidden'
}

/** Moves the cursor, taking it off the screen at once if the move is called off part-way. */
async function moving(move: (cursor: HTMLElement) => Promise<void>): Promise<void> {
  const cursor = document.getElementById(cursorID)
  if (cursor == null) return
  try {
    await move(cursor)
  } catch (error) {
    clearTimeout(fading)
    fade(cursor)
    throw error
  }
}

/**
 * Shows the cursor going to the element and clicking it. The caller does what the click
 * stands for, unless the signal aborts first: then this throws and nothing is to be clicked.
 */
export function clickWithCursor(target: Element, signal: AbortSignal): Promise<void> {
  return moving(async (cursor) => {
    if (!inViewport(target)) target.scrollIntoView({ block: 'center' })
    const to = target.getBoundingClientRect()
    await travel(cursor, to.left + Math.min(to.width / 2, 40), to.top + to.height / 2, signal)
    await wait(restMs, signal)
    cursor.dataset.state = 'pressing'
    await wait(pressMs, signal)
    cursor.dataset.state = 'moving'
  })
}

/** Shows the cursor going to the element's corner, where it does not cover what it points at. */
export function pointWithCursor(target: Element, signal: AbortSignal): Promise<void> {
  return moving(async (cursor) => {
    if (!inViewport(target)) target.scrollIntoView({ block: 'center' })
    const to = target.getBoundingClientRect()
    await travel(cursor, Math.min(to.right - 10, window.innerWidth - 40), to.bottom - 8, signal)
  })
}

/** Lets the cursor fade once it has rested a moment; another click keeps it on screen. */
export function releaseCursor() {
  const cursor = document.getElementById(cursorID)
  if (cursor == null) return
  clearTimeout(fading)
  fading = setTimeout(() => {
    fade(cursor)
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
