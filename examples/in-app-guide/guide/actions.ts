// Runs the agent's custom tools in the user's browser: show_step (point and wait for the
// user), click, fill and submit (do it for them). Each returns a fresh snapshot so the
// agent can plan the next step from what is actually on screen.

import { Overlay, sleep } from './overlay'
import { isPrivate, resolveRef, shownName, takeSnapshot } from './snapshot'

export type ToolCall = { id: string; name: string; input: Record<string, unknown> }
export type ToolResult = { outcome: 'succeeded' | 'failed'; text: string }

type Ctx = { overlay: Overlay; host: Element; signal: AbortSignal }

const INTERRUPTED = 'The user sent a new message in the guide before this step was done. It was not done. Read their message before continuing.'

export async function runTool(call: ToolCall, ctx: Ctx): Promise<ToolResult> {
  const ref = String(call.input.ref ?? '')
  const text = String(call.input.text ?? '')
  const target = resolveRef(ref)
  if (!target) {
    return result('target_missing', `No visible element has ref ${ref} any more. Use a ref from this snapshot.`, ctx)
  }
  try {
    switch (call.name) {
      case 'show_step':
        return await showStep(target, text, ctx)
      case 'click':
      case 'submit':
        await ctx.overlay.moveTo(target)
        ctx.overlay.highlight(target, text)
        await sleep(350)
        await ctx.overlay.press()
        // Cancelled while the cursor was on its way: stop before touching the page.
        if (ctx.signal.aborted) return interrupted(ctx)
        const destination = linkDestination(target)
        // Name it before clicking: React may reuse the same button for the next step's label.
        const label = describeShort(target)
        realClick(target as HTMLElement)
        await settle(destination)
        ctx.overlay.clearHighlight()
        return result('done', `Clicked ${label}.`, ctx)
      case 'fill': {
        await ctx.overlay.moveTo(target)
        if (ctx.signal.aborted) return interrupted(ctx)
        ctx.overlay.highlight(target, text)
        await fillValue(target, String(call.input.value ?? ''), ctx.signal)
        if (ctx.signal.aborted) return interrupted(ctx)
        await settle()
        ctx.overlay.clearHighlight()
        return result('done', `Filled ${describeShort(target)}.`, ctx)
      }
      default:
        return { outcome: 'failed', text: `Unknown tool ${call.name}.` }
    }
  } catch (err) {
    ctx.overlay.clearHighlight()
    return { outcome: 'failed', text: `${err instanceof Error ? err.message : String(err)}\n\nCurrent page:\n${takeSnapshot(ctx.host).text}` }
  }
}

async function showStep(target: Element, text: string, ctx: Ctx): Promise<ToolResult> {
  // A dropdown built from a button (Radix, shadcn): the step is done when its value changes.
  const combo = isComboButton(target)
  const comboBefore = combo ? comboValue(target) : ''
  const field = !combo && isField(target)
  const destination = linkDestination(target)
  await ctx.overlay.moveTo(target)
  // The abort listener below only hears a cancel that comes after this point.
  if (ctx.signal.aborted) return interrupted(ctx)

  let elsewhere: string | null = null
  const outcome = await new Promise<{ outcome: string; note: string }>((resolve) => {
    let finished = false
    const finish = (outcome: string, note: string) => {
      if (finished) return
      finished = true
      document.removeEventListener('click', onClick, true)
      document.removeEventListener('pointerdown', onPointerDown, true)
      document.removeEventListener('pointerup', onPointerUp, true)
      target.removeEventListener('change', onChange)
      target.removeEventListener('keydown', onKey as EventListener)
      clearInterval(watch)
      ctx.signal.removeEventListener('abort', onAbort)
      resolve({ outcome, note })
    }
    const onClick = (e: Event) => {
      const t = e.target as Element | null
      if (!t || ctx.host.contains(t)) return
      if (target.contains(t)) {
        if (!field && !combo) finish('done', `The user clicked ${describeShort(target)}.`)
        return
      }
      if (combo && t.closest('[role="option"],[role="listbox"]')) return
      const other = t.closest('a[href],button,[role="button"],[role="link"],[role="tab"],[role="menuitem"],[role="option"],input,select')
      if (other) {
        elsewhere = linkDestination(other)
        finish('clicked_elsewhere', `The user clicked ${describeShort(other)} instead.`)
      }
    }
    // Some controls act on press, not on click: a menu button that opens on pointerdown
    // (Radix, shadcn) covers itself before the mouse comes up, and a menu item that acts
    // on pointerup is removed before the click. Neither sends a click on the target, so
    // a press and release that started on the target also counts as doing the step.
    let pressed = false
    let comboOpened = false
    const onPointerDown = (e: Event) => {
      const t = e.target as Element | null
      pressed = !!t && !ctx.host.contains(t) && target.contains(t) && !field && !combo
    }
    const onPointerUp = () => {
      if (!pressed) return
      pressed = false
      // Let the click arrive first when there is one; it reports the same thing.
      setTimeout(() => finish('done', `The user clicked ${describeShort(target)}.`), 80)
    }
    const onChange = () => {
      if (target instanceof HTMLSelectElement) finish('done', `The user chose ${chosen(target, target.selectedOptions[0]?.label ?? '')}.`)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Enter' && target instanceof HTMLInputElement) finish('done', `The user entered ${fieldValue(target)} in ${describeShort(target)} and pressed Enter.`)
    }
    const onAbort = () => finish('interrupted', INTERRUPTED)
    const watch = setInterval(() => {
      if (!target.isConnected || !hasBox(target)) finish('page_changed', 'The highlighted element is gone; the page changed.')
      else if (combo) {
        // Done when the list has been opened and closed again, or the value changed.
        // Picking the option that was already selected is still an answer.
        const open = target.getAttribute('aria-expanded') === 'true'
        if (open) comboOpened = true
        else if (comboValue(target) !== comboBefore) finish('done', `The user chose ${chosen(target, comboValue(target))}.`)
        else if (comboOpened) finish('done', `The user opened ${describeShort(target)} and left it on ${chosen(target, comboValue(target))}.`)
      }
    }, 300)

    document.addEventListener('click', onClick, true)
    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('pointerup', onPointerUp, true)
    target.addEventListener('change', onChange)
    target.addEventListener('keydown', onKey as EventListener)
    ctx.signal.addEventListener('abort', onAbort)

    const actions = [
      ...(field && !(target instanceof HTMLSelectElement) ? [{ label: 'Next', primary: true, onClick: () => finish('done', `The user entered ${fieldValue(target)} in ${describeShort(target)} and pressed Next.`) }] : []),
      { label: 'Skip', onClick: () => finish('skipped', 'The user skipped this step.') },
      { label: 'Stop', onClick: () => finish('stopped', 'The user stopped the walkthrough.') },
    ]
    ctx.overlay.highlight(target, text, actions)
  })

  ctx.overlay.clearHighlight()
  if (outcome.outcome !== 'interrupted' && outcome.outcome !== 'stopped') await settle(outcome.outcome === 'done' ? destination : outcome.outcome === 'clicked_elsewhere' ? elsewhere : null)
  if (outcome.outcome === 'stopped') ctx.overlay.hideCursor()
  return result(outcome.outcome, outcome.note, ctx)
}

function interrupted(ctx: Ctx): ToolResult {
  ctx.overlay.clearHighlight()
  return result('interrupted', INTERRUPTED, ctx)
}

function result(outcome: string, note: string, ctx: Ctx): ToolResult {
  return { outcome: 'succeeded', text: `outcome: ${outcome}\n${note}\n\nCurrent page:\n${takeSnapshot(ctx.host).text}` }
}

function hasBox(el: Element) {
  const r = el.getBoundingClientRect()
  return r.width > 0 || r.height > 0
}

function isComboButton(el: Element) {
  return el.getAttribute('role') === 'combobox' && !(el instanceof HTMLInputElement)
}

function comboValue(el: Element) {
  return ((el as HTMLElement).innerText ?? '').replace(/\s+/g, ' ').trim()
}

/** How to name what the user picked, without repeating a private value. */
function chosen(el: Element, value: string) {
  return isPrivate(el) ? 'an option (not shown)' : `"${value}"`
}

/** Open a button-style dropdown and pick the option whose text matches. */
async function chooseOption(el: HTMLElement, value: string, signal: AbortSignal) {
  const norm = (x: string) => x.replace(/\s+/g, ' ').trim().toLowerCase()
  if (el.getAttribute('aria-expanded') !== 'true') realClick(el)
  let options: HTMLElement[] = []
  for (let i = 0; i < 15 && !options.length; i++) {
    await sleep(100)
    const listId = el.getAttribute('aria-controls')
    const list = (listId && document.getElementById(listId)) || document
    options = [...list.querySelectorAll<HTMLElement>('[role="option"]')].filter(hasBox)
  }
  const option = options.find((o) => norm(o.innerText) === norm(value)) ?? options.find((o) => norm(o.innerText).includes(norm(value)))
  if (!option || signal.aborted) {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    if (signal.aborted) return
    throw new Error(options.length ? noOption(el, value, options.map((o) => o.innerText.trim())) : `${describeShort(el)} did not open a list of options; use click on it instead.`)
  }
  realClick(option)
}

function isField(el: Element) {
  return (
    el instanceof HTMLTextAreaElement ||
    el instanceof HTMLSelectElement ||
    (el instanceof HTMLInputElement && !['button', 'submit', 'reset', 'checkbox', 'radio'].includes(el.type)) ||
    el.getAttribute('contenteditable') === 'true' ||
    ['textbox', 'searchbox', 'combobox'].includes(el.getAttribute('role') ?? '')
  )
}

function fieldValue(el: Element) {
  if (el instanceof HTMLInputElement && el.type === 'password') return 'a password'
  if (isPrivate(el)) return 'a value (not shown)'
  const v = el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement ? el.value : (el.textContent ?? '')
  return v.trim() ? `"${v.trim().slice(0, 200)}"` : 'nothing'
}

function describeShort(el: Element) {
  const name = shownName(el)
  return name ? `"${name}"` : `a ${el.tagName.toLowerCase()}`
}

function realClick(el: HTMLElement) {
  const r = el.getBoundingClientRect()
  const base = { bubbles: true, cancelable: true, composed: true, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2, button: 0, view: window }
  const pointer = { ...base, pointerId: 1, pointerType: 'mouse', isPrimary: true }
  el.dispatchEvent(new PointerEvent('pointerdown', pointer))
  el.dispatchEvent(new MouseEvent('mousedown', base))
  el.focus?.({ preventScroll: true })
  el.dispatchEvent(new PointerEvent('pointerup', pointer))
  el.dispatchEvent(new MouseEvent('mouseup', base))
  el.click()
}

/** The error for a value that isn't in a dropdown. A private dropdown's options are not listed. */
function noOption(el: Element, value: string, options: string[]) {
  return isPrivate(el) ? `No option "${value}" in ${describeShort(el)}.` : `No option "${value}". The options are: ${options.join(', ')}.`
}

async function fillValue(el: Element, value: string, signal: AbortSignal) {
  if (isComboButton(el)) return chooseOption(el as HTMLElement, value, signal)
  if (el instanceof HTMLSelectElement) {
    const norm = (s: string) => s.trim().toLowerCase()
    const options = [...el.options]
    const option =
      options.find((o) => norm(o.label) === norm(value) || norm(o.value) === norm(value)) ??
      options.find((o) => norm(o.label).includes(norm(value)))
    if (!option) throw new Error(noOption(el, value, options.map((o) => o.label)))
    setNativeValue(el, option.value)
    el.dispatchEvent(new Event('input', { bubbles: true }))
    el.dispatchEvent(new Event('change', { bubbles: true }))
    return
  }
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    el.focus({ preventScroll: true })
    const typeable = el instanceof HTMLTextAreaElement || ['text', 'search', 'email', 'tel', 'url', ''].includes(el.type)
    if (!typeable) {
      setNativeValue(el, value)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    } else {
      setNativeValue(el, '')
      el.dispatchEvent(new Event('input', { bubbles: true }))
      for (const ch of value) {
        // Cancelled while typing: stop where it is.
        if (signal.aborted) return
        setNativeValue(el, el.value + ch)
        el.dispatchEvent(new Event('input', { bubbles: true }))
        await sleep(value.length > 40 ? 6 : 28)
      }
    }
    el.dispatchEvent(new Event('change', { bubbles: true }))
    return
  }
  if (el instanceof HTMLElement && el.isContentEditable) {
    el.focus()
    el.textContent = value
    el.dispatchEvent(new InputEvent('input', { bubbles: true }))
    return
  }
  throw new Error(`${describeShort(el)} is not a text field or select; use click for it.`)
}

function setNativeValue(el: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(el, value)
}

/** Where a click on this element navigates within the app, if anywhere. */
function linkDestination(el: Element): string | null {
  const a = el.closest('a[href]') as HTMLAnchorElement | null
  if (!a || (a.target && a.target !== '_self')) return null
  const url = new URL(a.href, location.href)
  const here = location.pathname + location.search
  const there = url.pathname + url.search
  return url.origin === location.origin && there !== here ? there : null
}

/**
 * Wait until the page stops changing and has no requests in flight. When a link click
 * navigates (client-side routing can take a few seconds), first wait for the URL to change.
 */
export async function settle(destination: string | null = null, maxMs = 6000, quietMs = 350): Promise<void> {
  if (destination) {
    const start = performance.now()
    while (location.pathname + location.search !== destination && performance.now() - start < 10000) await sleep(80)
  }
  return new Promise((resolve) => {
    const start = performance.now()
    let last = start
    const observer = new MutationObserver(() => {
      last = performance.now()
    })
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true })
    const tick = () => {
      const now = performance.now()
      if (pendingRequests() > 0 && now - start < maxMs) {
        last = now
        setTimeout(tick, 50)
      } else if (now - last >= quietMs || now - start >= maxMs) {
        observer.disconnect()
        resolve()
      } else setTimeout(tick, 50)
    }
    setTimeout(tick, 120)
  })
}

// Counts the page's in-flight fetch requests, so the guide can tell "the page is quiet"
// from "the page is waiting on the server" (client-side navigations, form saves). The
// guide's own calls and long-lived streams are not counted.

let installed = false
let inflight = 0
const ignored: string[] = []

export function trackRequests(ignorePrefix: string) {
  if (!ignored.includes(ignorePrefix)) ignored.push(ignorePrefix)
  if (installed || typeof window === 'undefined') return
  installed = true
  const original = window.fetch
  window.fetch = function (input: RequestInfo | URL, init?: RequestInit) {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const accept = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined)).get('accept') ?? ''
    if (ignored.some((p) => url.includes(p)) || accept.includes('text/event-stream')) return original.call(this, input, init)
    inflight++
    let done = false
    const finish = () => {
      if (!done) {
        done = true
        inflight--
      }
    }
    // A request that never ends must not block the guide for ever.
    setTimeout(finish, 8000)
    return original.call(this, input, init).finally(finish)
  }
}

const pendingRequests = () => inflight
