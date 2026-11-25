// What the user can see, as text the model can read: headings, visible text, table rows
// and every control by role and name, in reading order. Anything typed into a field or
// editor is left out. Adapted from examples/in-app-guide/guide/snapshot.ts.

const interactive = [
  'a[href]',
  'button',
  'input:not([type=hidden])',
  'select',
  'textarea',
  'summary',
  '[contenteditable="true"]',
  '.monaco-editor',
  ...[
    'button',
    'link',
    'tab',
    'menuitem',
    'menuitemcheckbox',
    'menuitemradio',
    'option',
    'checkbox',
    'radio',
    'switch',
    'combobox',
    'textbox',
    'searchbox',
    'slider',
  ].map((role) => `[role="${role}"]`),
].join(',')
const heading = 'h1,h2,h3,h4,[role="heading"]'
const notice = '[role="alert"],[role="status"]'
const landmark =
  'nav,header,aside,main,footer,[role="navigation"],[role="banner"],[role="complementary"],[role="main"],[role="menu"],[role="listbox"],[role="tablist"]'
const landmarkNames = new Map([
  ['NAV', 'navigation'],
  ['HEADER', 'header'],
  ['ASIDE', 'sidebar'],
  ['MAIN', 'main'],
  ['FOOTER', 'footer'],
])
const skippedTags = new Set([
  'SCRIPT',
  'STYLE',
  'NOSCRIPT',
  'TEMPLATE',
  'svg',
  'SVG',
  'IMG',
  'VIDEO',
  'CANVAS',
  'IFRAME',
])
const stateAttributes = [
  ['aria-selected', 'selected', ''],
  ['aria-checked', 'checked', 'unchecked'],
  ['aria-pressed', 'pressed', ''],
  ['aria-expanded', 'expanded', 'collapsed'],
] as const

// The assistant's own launcher and panel are not part of the page, and a tooltip only
// repeats the name of the control under the pointer.
const notPage = '[data-assistant-surface],[role="tooltip"],[data-slot="tooltip-content"]'

const maxControls = 180
const maxTextLines = 140
const maxTableRows = 20
const maxName = 80
const maxCharacters = 16_000

function clean(text: string | null | undefined, max = 200): string {
  const single = (text ?? '').replaceAll(/\s+/g, ' ').trim()
  return single.length > max ? `${single.slice(0, max - 1)}…` : single
}

function isRendered(element: Element): boolean {
  // A display: contents wrapper has no box of its own, yet its children are on screen.
  return (
    element.checkVisibility({ opacityProperty: true, visibilityProperty: true }) ||
    getComputedStyle(element).display === 'contents'
  )
}

function isVisible(element: Element): boolean {
  const rect = element.getBoundingClientRect()
  return (rect.width > 0 || rect.height > 0) && isRendered(element)
}

function isHiddenFromUser(element: Element): boolean {
  return element.getAttribute('aria-hidden') === 'true' || element.hasAttribute('inert')
}

const typedContent =
  'textarea,select,[contenteditable="true"],.monaco-editor,[role="textbox"],[role="searchbox"]'

/** Holds what the user typed: its text is a value, never a name. */
function isTextEntry(element: Element): boolean {
  return element instanceof HTMLInputElement || element.matches(typedContent)
}

/** Acts like a control without saying so: a row or card with a click handler. */
function isLooseClickable(element: Element): boolean {
  if (element.parentElement?.closest(interactive)) return false
  if (getComputedStyle(element).cursor !== 'pointer') return false
  if (element.parentElement && getComputedStyle(element.parentElement).cursor === 'pointer') {
    return false
  }
  const tabindex = element.getAttribute('tabindex')
  const focusable = tabindex !== null && Number(tabindex) >= 0
  return focusable || !element.querySelector(interactive)
}

function hasOwnText(element: Element): boolean {
  if (element.children.length === 0) return clean(element.textContent) !== ''
  return [...element.childNodes].some(
    (node) => node.nodeType === Node.TEXT_NODE && clean(node.textContent) !== '',
  )
}

/** The element's displayed text, without anything typed into a field or editor inside it. */
function shownText(element: Element): string {
  if (element.querySelector(typedContent) != null) {
    return [...element.childNodes]
      .filter((node) => node.nodeType === Node.TEXT_NODE)
      .map((node) => node.textContent ?? '')
      .join(' ')
  }
  return element instanceof HTMLElement ? element.innerText : element.textContent
}

function labelText(element: Element): string {
  const label =
    (element.id === '' ? null : document.querySelector(`label[for="${CSS.escape(element.id)}"]`)) ??
    element.closest('label')
  return clean(label?.textContent, maxName)
}

function accessibleName(element: Element): string {
  const labelledBy = element.getAttribute('aria-labelledby')
  if (labelledBy != null) {
    const text = clean(
      labelledBy
        .split(/\s+/)
        .map((id) => document.getElementById(id)?.textContent ?? '')
        .join(' '),
      maxName,
    )
    if (text !== '') return text
  }
  const aria = clean(element.getAttribute('aria-label'), maxName)
  if (aria !== '') return aria
  const label = labelText(element)
  if (label !== '') return label
  if (isTextEntry(element)) {
    return clean(
      element.getAttribute('placeholder') ??
        element.getAttribute('title') ??
        element.getAttribute('name'),
      maxName,
    )
  }
  const text = clean(shownText(element), maxName)
  if (text !== '') return text
  return clean(
    element.getAttribute('title') ??
      element.querySelector('img[alt]')?.getAttribute('alt') ??
      element.querySelector('svg title')?.textContent,
    maxName,
  )
}

function roleOf(element: Element): string {
  const explicit = element.getAttribute('role')
  if (explicit != null) return explicit
  if (element instanceof HTMLAnchorElement) return 'link'
  if (element instanceof HTMLSelectElement) return 'select'
  if (element instanceof HTMLInputElement) {
    if (['checkbox', 'radio', 'range', 'file', 'date', 'number'].includes(element.type)) {
      return element.type
    }
    if (element.type === 'search') return 'searchbox'
    return ['button', 'submit', 'reset'].includes(element.type) ? 'button' : 'textbox'
  }
  if (element.tagName === 'SUMMARY' || element.tagName === 'BUTTON') return 'button'
  return isTextEntry(element) ? 'textbox' : 'clickable'
}

function rowCells(row: Element): string {
  return [...row.querySelectorAll('td,th')]
    .map((cell) => clean(shownText(cell), 60))
    .filter((cell) => cell !== '')
    .join(' | ')
}

// A button-style dropdown shows its choice as its text; it is named by its label instead.
function isDropdown(element: Element): boolean {
  return roleOf(element) === 'combobox' && !(element instanceof HTMLInputElement)
}

function controlName(element: Element): string {
  return isDropdown(element)
    ? clean(element.getAttribute('aria-label'), maxName) || labelText(element)
    : accessibleName(element)
}

function describe(element: Element): string {
  if (element.tagName === 'TR') return `row "${clean(rowCells(element), 160)}" (clickable)`
  const role = roleOf(element)
  const dropdown = isDropdown(element)
  const name = controlName(element)
  const parts = [name === '' ? role : `${role} "${name}"`]
  if (dropdown) parts.push(`value="${clean(shownText(element), 60)}"`)
  if (element instanceof HTMLSelectElement) {
    parts.push(`selected="${clean(element.selectedOptions[0]?.label, 40)}"`)
  }
  const valueText = clean(element.getAttribute('aria-valuetext'), 80)
  if (valueText !== '') parts.push(`value="${valueText}"`)
  if (element instanceof HTMLInputElement && ['checkbox', 'radio'].includes(element.type)) {
    parts.push(element.checked ? 'checked' : 'unchecked')
  }
  for (const [attribute, on, off] of stateAttributes) {
    const value = element.getAttribute(attribute)
    if (value === 'true') parts.push(on)
    else if (value === 'false' && off !== '') parts.push(off)
  }
  const current = element.getAttribute('aria-current')
  if (current != null && current !== 'false') parts.push('current')
  // Styling is the only place a page marks its main and its destructive action.
  if (element.classList.contains('bg-primary')) parts.push('primary')
  if (element.classList.contains('bg-destructive')) parts.push('destructive')
  if (element.matches(':disabled,[aria-disabled="true"]')) parts.push('disabled')
  const href = element instanceof HTMLAnchorElement ? element.getAttribute('href') : null
  if (href?.startsWith('/') === true) parts.push(`href=${href}`)
  const rect = element.getBoundingClientRect()
  if (rect.bottom < 0 || rect.top > window.innerHeight) parts.push('(off-screen)')
  return parts.join(' ')
}

function topDialog(): Element | null {
  const dialogs = [
    ...document.querySelectorAll('dialog[open],[role="dialog"],[role="alertdialog"]'),
  ].filter(
    (dialog) =>
      dialog.closest(notPage) == null &&
      isVisible(dialog) &&
      (dialog.matches('dialog[open]') || dialog.getAttribute('aria-modal') === 'true'),
  )
  return dialogs.at(-1) ?? null
}

function isShown(element: Element): boolean {
  return element.closest(`${notPage},[aria-hidden="true"],[inert]`) == null && isVisible(element)
}

/**
 * What the assistant may click for a name, the outline's own wording such as
 * `button "New provider"`, or a link's path such as `/models`. Anything the user could click
 * themselves counts; a heading does not, and neither does the assistant's own surface.
 */
export function findClickableOnPage(target: string): Element | null {
  const wanted = target.trim()
  if (!wanted.startsWith('/')) return findOnPage(wanted, (element) => !element.matches(heading))
  const pathname = wanted.split(/[?#]/, 1)[0]
  return (
    [...(topDialog() ?? document.body).querySelectorAll('a[href]')].find(
      (link) =>
        link instanceof HTMLAnchorElement &&
        link.origin === window.location.origin &&
        link.pathname === pathname &&
        isShown(link),
    ) ?? null
  )
}

/** The element as the outline words it, for example `button "New provider" primary`. */
export function describeOnPage(element: Element): string {
  return describe(element)
}

/**
 * The element a name from the outline refers to: a control, a table row or a heading.
 * Takes the bare name or the outline's own wording, such as `button "New model"`.
 */
export function findOnPage(
  target: string,
  only: (element: Element) => boolean = () => true,
): Element | null {
  const outline = /^(?:([a-z]+) )?"([^"]+)"/i.exec(target.trim())
  const role = outline?.[1]?.toLowerCase()
  const wanted = clean(outline?.[2] ?? target, 400).toLowerCase()
  if (wanted === '') return null
  const root = topDialog() ?? document.body
  const named = (selector: string, kind: string | null, name: (element: Element) => string) =>
    [...root.querySelectorAll(selector)]
      .filter((element) => isShown(element) && only(element))
      .map((element) => ({
        element,
        role: kind ?? roleOf(element),
        name: name(element).toLowerCase(),
      }))
  const all = [
    ...named(interactive, null, controlName),
    ...named('tbody tr', 'row', (row) => clean(rowCells(row), 160)),
    ...named(heading, 'heading', (element) => clean(shownText(element), 140)),
  ]
  const ofRole = all.filter((candidate) => candidate.role === role)
  const pool = ofRole.length > 0 ? ofRole : all
  const words = nameWords(wanted)
  const match =
    pool.find((candidate) => candidate.name === wanted) ??
    pool.find((candidate) => candidate.name.startsWith(wanted)) ??
    pool.find((candidate) => candidate.name.includes(wanted)) ??
    // A name given from memory: the same words in another order or with another verb.
    pool.find((candidate) => {
      const named = nameWords(candidate.name)
      return words.size > 0 && [...words].every((word) => named.has(word))
    })
  return match?.element ?? null
}

const fillerWords = new Set(['a', 'an', 'the', 'your', 'my', 'to', 'of', 'button', 'link', 'page'])
const sameVerb = new Map([
  ['add', 'new'],
  ['create', 'new'],
])

/** The words that identify a control, so that "Add a provider" and "New provider" agree. */
function nameWords(name: string): Set<string> {
  const words = name.toLowerCase().match(/[a-z0-9]+/g) ?? []
  return new Set(
    words.filter((word) => !fillerWords.has(word)).map((word) => sameVerb.get(word) ?? word),
  )
}

export function pageSnapshot(): string {
  const root = topDialog() ?? document.body
  const lines = [`URL: ${window.location.pathname}`, `Title: ${document.title}`]
  if (root !== document.body) {
    lines.push(
      `Open dialog: "${accessibleName(root) || 'untitled'}" (the page behind it is blocked until it closes)`,
    )
  }
  let controls = 0
  let textLines = 0
  let textParent: Element | null = null
  let textParts: string[] = []

  function flush() {
    if (textParts.length > 0) {
      lines.push(`text: ${clean(textParts.join(' · '), 200)}`)
      textLines++
    }
    textParent = null
    textParts = []
  }
  function push(line: string) {
    flush()
    lines.push(line)
  }
  function addText(parent: Element | null, text: string) {
    if (text === '' || textLines >= maxTextLines) return
    if (textParent !== parent) flush()
    textParent = parent
    textParts.push(text)
  }
  function addControl(element: Element) {
    if (!isVisible(element)) return
    controls++
    if (controls <= maxControls) push(describe(element))
  }
  function table(element: HTMLTableElement) {
    const heads = [...element.querySelectorAll('thead th')].map((cell) =>
      clean(cell.textContent, 30),
    )
    const rows = [...element.querySelectorAll('tbody tr')].filter(isRendered)
    const columns = heads.some((head) => head !== '') ? `: ${heads.join(' | ')}` : ''
    push(`table${columns} (${String(rows.length)} rows)`)
    rows.slice(0, maxTableRows).forEach((row) => {
      if (isLooseClickable(row)) {
        addControl(row)
      } else {
        const cells = rowCells(row)
        if (cells !== '') push(`  row: ${cells}`)
      }
      walk(row, false)
    })
    if (rows.length > maxTableRows) {
      push(`  (${String(rows.length - maxTableRows)} more rows not shown)`)
    }
  }
  // readText=false: this subtree's text was already written; only list its controls.
  function walk(node: Element, readText: boolean) {
    for (const child of node.children) {
      if (skippedTags.has(child.tagName) || child.matches(notPage)) continue
      if (!isRendered(child) || isHiddenFromUser(child)) continue
      if (child.matches(interactive)) {
        addControl(child)
      } else if (isLooseClickable(child)) {
        addControl(child)
        walk(child, false)
      } else if (!readText) {
        walk(child, false)
      } else if (child.matches(landmark)) {
        const role = child.getAttribute('role') ?? landmarkNames.get(child.tagName) ?? 'region'
        const name = child.getAttribute('aria-label')
        push(`-- ${role}${name == null ? '' : ` "${name}"`}`)
        walk(child, true)
        push(`-- end ${role}`)
      } else if (child.matches(heading)) {
        const level = /^H(\d)$/.exec(child.tagName)?.[1] ?? child.getAttribute('aria-level') ?? '2'
        const text = clean(shownText(child), 140)
        if (text !== '') push(`${'#'.repeat(Number(level))} ${text}`)
        walk(child, false)
      } else if (child.matches(notice)) {
        const text = clean(shownText(child), 200)
        if (text !== '') push(`notice: ${text}`)
        walk(child, false)
      } else if (child instanceof HTMLTableElement) {
        table(child)
      } else if (child.tagName === 'DL') {
        const pairs = [...child.querySelectorAll('dt')].map(
          (term) =>
            `${clean(shownText(term), 40)}: ${clean(term.nextElementSibling == null ? '' : shownText(term.nextElementSibling), 80)}`,
        )
        if (pairs.length > 0) push(`details: ${pairs.join('; ')}`)
        walk(child, false)
      } else if (hasOwnText(child)) {
        addText(child.parentElement, clean(shownText(child), 200))
        walk(child, false)
      } else {
        walk(child, true)
      }
    }
  }

  walk(root, true)
  flush()
  if (controls > maxControls) lines.push('(more controls than fit are not listed)')
  const text = lines.join('\n')
  return text.length > maxCharacters ? `${text.slice(0, maxCharacters)}\n(truncated)` : text
}
