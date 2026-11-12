// Reads the page the way the agent needs it, in reading order: the URL, any open dialog,
// headings, visible text (table rows, summaries, status badges, notices) and every
// visible interactive element with a short ref (e1, e2, …). The host app adds no markup
// for this; it works off standard HTML and ARIA, so the same code runs on a mock or on
// the customer's real app.

const INTERACTIVE = [
  'a[href]',
  'button',
  'input:not([type=hidden])',
  'select',
  'textarea',
  'summary',
  '[contenteditable="true"]',
  ...['button', 'link', 'tab', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'option', 'checkbox', 'radio', 'switch', 'combobox', 'textbox', 'searchbox'].map(
    (r) => `[role="${r}"]`,
  ),
].join(',')
const HEADING = 'h1,h2,h3,h4,[role="heading"]'
const NOTICE = '[role="alert"],[role="status"]'
const LANDMARK = 'nav,header,aside,main,footer,[role="navigation"],[role="banner"],[role="complementary"],[role="main"],[role="menu"],[role="listbox"],[role="tablist"]'
const SKIP = new Set(['SCRIPT', 'STYLE', 'NOSCRIPT', 'TEMPLATE', 'svg', 'SVG', 'IMG', 'VIDEO', 'CANVAS', 'IFRAME'])

// What must not leave the page. The host app marks a field or a region with
// data-guide-private; password, card and one-time-code fields are private by themselves.
const PRIVATE = '[data-guide-private]'
const PRIVATE_AUTOCOMPLETE = new Set(['cc-number', 'cc-csc', 'one-time-code', 'current-password', 'new-password'])

const MAX_ELEMENTS = 180
const MAX_TEXT_LINES = 140
const MAX_TABLE_ROWS = 20
const MAX_NAME = 80

export type Snapshot = { text: string; refs: Map<string, Element> }

let current: Snapshot = { text: '', refs: new Map() }

/** The element a ref pointed to in the most recent snapshot, if it is still on the page. */
export function resolveRef(ref: string): Element | null {
  const el = current.refs.get(ref.trim().replace(/^\[|\]$/g, ''))
  return el && el.isConnected && isVisible(el) ? el : null
}

/** True when this element's value and text must stay out of snapshots and step results. */
export function isPrivate(el: Element): boolean {
  if (el.closest(PRIVATE)) return true
  if (!(el instanceof HTMLInputElement)) return false
  if (el.type === 'password') return true
  return (el.getAttribute('autocomplete') ?? '').toLowerCase().split(/\s+/).some((token) => PRIVATE_AUTOCOMPLETE.has(token))
}

/** An element's text with anything private inside it left out. `rendered`: as displayed (innerText). */
function textOf(el: Element | null | undefined, rendered = false): string {
  if (!el) return ''
  if (!el.querySelector(PRIVATE)) return (rendered ? (el as HTMLElement).innerText : null) ?? el.textContent ?? ''
  const copy = el.cloneNode(true) as Element
  for (const part of copy.querySelectorAll(PRIVATE)) part.replaceWith('(private)')
  return copy.textContent ?? ''
}

export function takeSnapshot(ignore?: Element | null): Snapshot {
  const root = topDialog() ?? document.body
  const lines: string[] = [`URL: ${location.pathname}${location.search}`, `Title: ${document.title}`]
  if (root !== document.body) {
    lines.push(`Open dialog: "${accessibleName(root) || 'untitled'}" (the page behind it is blocked until it closes)`)
  }
  const refs = new Map<string, Element>()
  let elements = 0
  let textLines = 0
  let truncated = false
  let pending: { parent: Element | null; parts: string[] } = { parent: null, parts: [] }

  const flush = () => {
    if (pending.parts.length) {
      lines.push(`text: ${clean(pending.parts.join(' · '), 200)}`)
      textLines++
    }
    pending = { parent: null, parts: [] }
  }
  const push = (line: string) => {
    flush()
    lines.push(line)
  }
  const addText = (parent: Element | null, text: string) => {
    if (!text || textLines >= MAX_TEXT_LINES) return
    if (pending.parent !== parent) flush()
    pending.parent = parent
    pending.parts.push(text)
  }
  const addElement = (el: Element) => {
    if (elements >= MAX_ELEMENTS) {
      truncated = true
      return
    }
    const ref = `e${++elements}`
    refs.set(ref, el)
    push(`[${ref}] ${describe(el)}`)
  }

  // inTextMode=false: the text of this subtree was already emitted, or is private; only list controls.
  const walk = (node: Element, inTextMode: boolean) => {
    for (const child of node.children) {
      if (SKIP.has(child.tagName) || child === ignore || ignore?.contains(child)) continue
      if (!isRendered(child) || isHiddenFromUser(child)) continue
      // A private field or region: list its controls, leave out its text.
      const textMode = inTextMode && !child.matches(PRIVATE)

      if (child.matches(INTERACTIVE)) {
        if (isVisible(child)) addElement(child)
        continue
      }
      // Clickable things without a role: rows and cards wired up with onClick.
      if (isLooseClickable(child)) {
        if (isVisible(child)) addElement(child)
        walk(child, false)
        continue
      }
      if (textMode && child.matches(LANDMARK)) {
        const role = child.getAttribute('role') ?? { NAV: 'navigation', HEADER: 'header', ASIDE: 'sidebar', MAIN: 'main', FOOTER: 'footer' }[child.tagName] ?? ''
        const name = child.getAttribute('aria-label')
        push(`-- ${role}${name ? ` "${name}"` : ''}`)
        walk(child, true)
        push(`-- end ${role}`)
        continue
      }
      if (textMode && child.matches(HEADING)) {
        const level = child.tagName.match(/^H(\d)$/)?.[1] ?? child.getAttribute('aria-level') ?? '2'
        const text = clean(textOf(child), 140)
        if (text) push(`${'#'.repeat(Number(level))} ${text}`)
        walk(child, false)
        continue
      }
      if (textMode && child.matches(NOTICE)) {
        const text = clean(textOf(child, true), 200)
        if (text) push(`notice: ${text}`)
        walk(child, false)
        continue
      }
      if (textMode && child.tagName === 'TABLE') {
        table(child as HTMLTableElement)
        continue
      }
      if (textMode && child.tagName === 'DL') {
        const pairs = [...child.querySelectorAll('dt')].map((dt) => `${clean(textOf(dt), 40)}: ${clean(textOf(dt.nextElementSibling), 80)}`)
        if (pairs.length) push(`details: ${pairs.join('; ')}`)
        walk(child, false)
        continue
      }
      if (textMode && hasOwnText(child)) {
        // Mixed inline content ("Current rate: <b>$6,500</b>"): one line for the block.
        addText(child.parentElement, clean(textOf(child, true), 200))
        walk(child, false)
        continue
      }
      walk(child, textMode)
    }
  }

  const table = (t: HTMLTableElement) => {
    const heads = [...t.querySelectorAll('thead th')].map((th) => clean(th.textContent, 30))
    const rows = [...t.querySelectorAll('tbody tr')].filter((r) => isRendered(r))
    push(`table${heads.some(Boolean) ? `: ${heads.join(' | ')}` : ''} (${rows.length} rows)`)
    rows.forEach((row, i) => {
      if (i < MAX_TABLE_ROWS) {
        if (isLooseClickable(row) && isVisible(row)) addElement(row)
        else {
          const cells = rowCells(row)
          if (cells) push(`  row: ${cells}`)
        }
      }
      walk(row, false)
    })
    if (rows.length > MAX_TABLE_ROWS) push(`  (${rows.length - MAX_TABLE_ROWS} more rows not shown)`)
  }

  walk(root, true)
  flush()
  if (truncated) lines.push(`(more elements not listed; the page has more controls than fit)`)
  current = { text: lines.join('\n'), refs }
  return current
}

function topDialog(): Element | null {
  const dialogs = [...document.querySelectorAll('dialog[open],[role="dialog"],[role="alertdialog"]')].filter(
    (d) => isVisible(d) && (d.matches('dialog[open]') || d.getAttribute('aria-modal') === 'true'),
  )
  return dialogs.at(-1) ?? null
}

/** Has a box and isn't hidden by CSS. */
export function isVisible(el: Element): boolean {
  const rect = el.getBoundingClientRect()
  if (rect.width === 0 && rect.height === 0) return false
  return isRendered(el)
}

function isRendered(el: Element): boolean {
  if (typeof el.checkVisibility === 'function') return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })
  const style = getComputedStyle(el)
  return style.visibility !== 'hidden' && style.display !== 'none'
}

function isHiddenFromUser(el: Element): boolean {
  return el.getAttribute('aria-hidden') === 'true' || el.hasAttribute('inert')
}

/**
 * An element that acts like a control without saying so: it has a pointer cursor (and
 * its parent doesn't), and it is either keyboard-focusable or has no real controls
 * inside. Catches table rows and cards with onClick handlers; skips plain containers.
 */
function isLooseClickable(el: Element): boolean {
  if (el.parentElement?.closest(INTERACTIVE)) return false
  const style = getComputedStyle(el)
  if (style.cursor !== 'pointer') return false
  if (el.parentElement && getComputedStyle(el.parentElement).cursor === 'pointer') return false
  const tabindex = el.getAttribute('tabindex')
  const focusable = tabindex !== null && Number(tabindex) >= 0
  return focusable || !el.querySelector(INTERACTIVE)
}

function rowCells(row: Element): string {
  if (isPrivate(row)) return '(private)'
  const cells = [...row.querySelectorAll('td,th')].map((c) => (c.matches(PRIVATE) ? '(private)' : clean(textOf(c, true), 60)))
  return cells.some(Boolean) ? cells.join(' | ') : ''
}

function hasOwnText(el: Element): boolean {
  if (!el.children.length) return !!el.textContent?.trim()
  for (const n of el.childNodes) if (n.nodeType === Node.TEXT_NODE && n.textContent?.trim()) return true
  return false
}

function describe(el: Element): string {
  if (el.tagName === 'TR') return `row "${clean(rowCells(el), 160)}" (clickable)`
  const role = roleOf(el)
  const hidden = isPrivate(el)
  // A button-style dropdown shows its value as its text; name it by its label instead.
  const dropdown = role === 'combobox' && !(el instanceof HTMLInputElement)
  const name = dropdown ? labelFor(el) : shownName(el)
  const parts = [`${role}${name ? ` "${name}"` : ''}`]
  if (hidden && !name) parts.push('(private)')
  if (dropdown) parts.push(hidden ? 'value=(hidden)' : `value="${clean((el as HTMLElement).innerText, 60)}"`)

  if (hidden && (el instanceof HTMLSelectElement || el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement)) {
    if (!(el instanceof HTMLInputElement && el.type === 'file')) parts.push('value=(hidden)')
  } else if (el instanceof HTMLSelectElement) {
    const options = [...el.options].map((o) => clean(o.label || o.text, 40))
    parts.push(`selected="${clean(el.selectedOptions[0]?.label ?? '', 40)}"`)
    parts.push(`options=[${options.slice(0, 15).join(', ')}${options.length > 15 ? ', …' : ''}]`)
  } else if (el instanceof HTMLInputElement && (el.type === 'checkbox' || el.type === 'radio')) {
    parts.push(el.checked ? 'checked' : 'unchecked')
  } else if (el instanceof HTMLInputElement && el.type === 'file') {
    parts.push(el.files?.length ? `files=${el.files.length}` : 'no file chosen')
  } else if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    parts.push(`value="${clean(el.value, 60)}"`)
    if (el instanceof HTMLInputElement && el.type === 'date') parts.push('(format YYYY-MM-DD)')
    if (el.required) parts.push('required')
  }
  for (const [attr, label] of [
    ['aria-selected', 'selected'],
    ['aria-checked', 'checked'],
    ['aria-pressed', 'pressed'],
    ['aria-expanded', 'expanded'],
  ] as const) {
    const v = el.getAttribute(attr)
    if (v === 'true') parts.push(label)
    else if (v === 'false' && (attr === 'aria-expanded' || attr === 'aria-checked')) parts.push(attr === 'aria-expanded' ? 'collapsed' : 'unchecked')
  }
  if (el.getAttribute('aria-current') && el.getAttribute('aria-current') !== 'false') parts.push('current')
  if ((el as HTMLButtonElement).disabled || el.getAttribute('aria-disabled') === 'true') parts.push('disabled')
  if (!hidden && el instanceof HTMLAnchorElement && el.getAttribute('href')?.startsWith('/')) parts.push(`href=${el.getAttribute('href')}`)

  const rect = el.getBoundingClientRect()
  if (rect.bottom < 0 || rect.top > innerHeight) parts.push('(off-screen)')
  return parts.join(' ')
}

function roleOf(el: Element): string {
  const explicit = el.getAttribute('role')
  if (explicit) return explicit
  if (el instanceof HTMLAnchorElement) return 'link'
  if (el instanceof HTMLSelectElement) return 'select'
  if (el instanceof HTMLTextAreaElement) return 'textbox'
  if (el instanceof HTMLInputElement) {
    if (['checkbox', 'radio', 'range', 'file', 'date', 'number'].includes(el.type)) return el.type
    if (el.type === 'search') return 'searchbox'
    if (['button', 'submit', 'reset'].includes(el.type)) return 'button'
    return 'textbox'
  }
  if (el.tagName === 'SUMMARY' || el.tagName === 'BUTTON') return 'button'
  if ((el as HTMLElement).isContentEditable) return 'textbox'
  return 'clickable'
}

/**
 * The name to send to the agent. A button's or a field's label is part of the app, so it
 * is kept; where the name would be the content itself (a link's text, a clickable row or
 * card, an option, or any control the host marked private directly) it is left out.
 */
export function shownName(el: Element): string {
  if (!isPrivate(el)) return accessibleName(el)
  const field = el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement
  if (el.matches(PRIVATE) ? !field : ['link', 'clickable', 'option', 'menuitem', 'menuitemcheckbox', 'menuitemradio'].includes(roleOf(el))) return ''
  return accessibleName(el)
}

export function accessibleName(el: Element): string {
  const labelledBy = el.getAttribute('aria-labelledby')
  if (labelledBy) {
    const text = labelledBy
      .split(/\s+/)
      .map((id) => document.getElementById(id)?.textContent ?? '')
      .join(' ')
    if (clean(text)) return clean(text, MAX_NAME)
  }
  const aria = el.getAttribute('aria-label')
  if (aria && clean(aria)) return clean(aria, MAX_NAME)
  if (!(el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement)) {
    const label = el.id ? document.querySelector(`label[for="${CSS.escape(el.id)}"]`) : null
    if (label && clean(label.textContent)) return clean(label.textContent, MAX_NAME)
  }
  if (el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement) {
    const label = (el.id && document.querySelector(`label[for="${CSS.escape(el.id)}"]`)) || el.closest('label')
    if (label) return clean(label.textContent, MAX_NAME)
    return clean(el.getAttribute('placeholder') ?? el.getAttribute('title') ?? el.getAttribute('name') ?? '', MAX_NAME)
  }
  const text = clean(textOf(el, true))
  if (text) return clean(text, MAX_NAME)
  return clean(el.getAttribute('title') ?? el.querySelector('img[alt]')?.getAttribute('alt') ?? el.querySelector('svg title')?.textContent ?? '', MAX_NAME)
}

function labelFor(el: Element): string {
  const aria = el.getAttribute('aria-label')
  if (aria && clean(aria)) return clean(aria, MAX_NAME)
  const label = el.id ? document.querySelector(`label[for="${CSS.escape(el.id)}"]`) : null
  return clean(label?.textContent, MAX_NAME)
}

function clean(s: string | null | undefined, max = 200): string {
  const t = (s ?? '').replace(/\s+/g, ' ').trim()
  return t.length > max ? `${t.slice(0, max - 1)}…` : t
}
