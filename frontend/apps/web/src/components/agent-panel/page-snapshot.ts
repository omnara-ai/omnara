// What the user can see, as text the model can read: headings, visible text, table rows
// and every control by role, name and ref, in reading order. Anything typed into a field
// or editor is left out, and so is any credential the page shows. Adapted from
// examples/in-app-guide/guide/snapshot.ts.

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
// A component that shows a credential as text marks it; see SecretText.
const secret = '[data-secret]'
const withheld = `${typedContent},${secret}`

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

/**
 * The element's displayed text. Every read of page text goes through here, so nothing typed
 * into a field or editor and no credential is read, however deep inside the element it sits.
 */
function shownText(element: Element | null | undefined): string {
  if (element == null || element.matches(withheld)) return ''
  if (element.querySelector(withheld) == null) {
    return element instanceof HTMLElement ? element.innerText : element.textContent
  }
  return [...element.childNodes]
    .map((node) => {
      if (node instanceof Element) return isRendered(node) ? shownText(node) : ''
      return node.nodeType === Node.TEXT_NODE ? (node.textContent ?? '') : ''
    })
    .join(' ')
}

function labelText(element: Element): string {
  const label =
    (element.id === '' ? null : document.querySelector(`label[for="${CSS.escape(element.id)}"]`)) ??
    element.closest('label')
  return clean(shownText(label), maxName)
}

function accessibleName(element: Element): string {
  const labelledBy = element.getAttribute('aria-labelledby')
  if (labelledBy != null) {
    const text = clean(
      labelledBy
        .split(/\s+/)
        .map((id) => shownText(document.getElementById(id)))
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

// A control keeps one ref for as long as it stays in the document. The outline prints it, so
// the assistant can say which of several controls that read the same it means.
const refs = new WeakMap<Element, string>()
const referenced = new Map<string, WeakRef<Element>>()
let lastRef = 0

function refOf(element: Element): string {
  let ref = refs.get(element)
  if (ref == null) {
    lastRef += 1
    ref = `e${String(lastRef)}`
    refs.set(element, ref)
  }
  referenced.set(ref, new WeakRef(element))
  return ref
}

function describe(element: Element): string {
  if (element.tagName === 'TR') {
    return `row "${clean(rowCells(element), 160)}" (clickable) [${refOf(element)}]`
  }
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
  parts.push(`[${refOf(element)}]`)
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

/** What a name or a ref from the outline comes to on the page as it is now. */
export type PageMatch =
  | { kind: 'one'; element: Element }
  /** Several different things answer to the name; these are their refs. */
  | { kind: 'several'; refs: string[] }
  /** The ref belongs to something that is no longer on the screen. */
  | { kind: 'gone' }
  | { kind: 'none' }

/**
 * What the assistant may click for a ref, a name, the outline's own wording such as
 * `button "New provider"`, or a link's path such as `/models`. Anything the user could click
 * themselves counts; a heading does not, and neither does the assistant's own surface.
 */
export function matchClickableOnPage(target: string): PageMatch {
  const wanted = target.trim()
  if (!wanted.startsWith('/')) return matchOnPage(wanted, (element) => !element.matches(heading))
  const pathname = wanted.split(/[?#]/, 1)[0]
  const link = [...(topDialog() ?? document.body).querySelectorAll('a[href]')].find(
    (candidate) =>
      candidate instanceof HTMLAnchorElement &&
      candidate.origin === window.location.origin &&
      candidate.pathname === pathname &&
      isShown(candidate),
  )
  return link == null ? { kind: 'none' } : { kind: 'one', element: link }
}

/** The element as the outline words it, for example `button "New provider" primary [e12]`. */
export function describeOnPage(element: Element): string {
  return describe(element)
}

// What a name can refer to, most specific first: a control, then a table row, then a heading.
const kinds = [
  { selector: interactive, role: null, name: controlName },
  { selector: 'tbody tr', role: 'row', name: (row: Element) => clean(rowCells(row), 160) },
  {
    selector: heading,
    role: 'heading',
    name: (element: Element) => clean(shownText(element), 140),
  },
]

/**
 * The different things a set of matches stands for. A control beats the row or heading that
 * merely contains or repeats its name, and two links to the same place are one destination.
 */
function distinct(matches: { element: Element; kind: number }[]): Element[] {
  const best = Math.min(...matches.map((match) => match.kind))
  const elements = [
    ...new Set(matches.filter((match) => match.kind === best).map((match) => match.element)),
  ]
  const destinations = new Set<string>()
  return elements
    .filter((element) => !elements.some((other) => other !== element && element.contains(other)))
    .filter((element) => {
      if (!(element instanceof HTMLAnchorElement)) return true
      if (destinations.has(element.href)) return false
      destinations.add(element.href)
      return true
    })
}

/**
 * What a ref or a name from the outline refers to: a control, a table row or a heading. A
 * name may be bare or in the outline's own wording, such as `button "New model"`. A name
 * that fits several different things is not resolved to the first of them.
 */
export function matchOnPage(
  target: string,
  only: (element: Element) => boolean = () => true,
): PageMatch {
  const wanted = target.trim()
  const root = topDialog() ?? document.body
  const ref = (/^\[?(e\d+)\]?$/.exec(wanted) ?? /\[(e\d+)\]$/.exec(wanted))?.[1]
  if (ref != null) {
    const element = referenced.get(ref)?.deref()
    if (element == null || !root.contains(element) || !isShown(element)) return { kind: 'gone' }
    return only(element) ? { kind: 'one', element } : { kind: 'none' }
  }
  const outline = /^(?:([a-z]+) )?"([^"]+)"/i.exec(wanted)
  const role = outline?.[1]?.toLowerCase()
  const name = clean(outline?.[2] ?? wanted, 400).toLowerCase()
  if (name === '') return { kind: 'none' }
  const all = kinds.flatMap((kind, index) =>
    [...root.querySelectorAll(kind.selector)]
      .filter((element) => isShown(element) && only(element))
      .map((element) => ({
        element,
        kind: index,
        role: kind.role ?? roleOf(element),
        name: kind.name(element).toLowerCase(),
      })),
  )
  const ofRole = all.filter((candidate) => candidate.role === role)
  const pool = ofRole.length > 0 ? ofRole : all
  const words = nameWords(name)
  const closeness: ((candidate: string) => boolean)[] = [
    (candidate) => candidate === name,
    (candidate) => candidate.startsWith(name),
    (candidate) => candidate.includes(name),
    // A name given from memory: the same words in another order or with another verb,
    // then those words among others.
    (candidate) => {
      const named = nameWords(candidate)
      return words.size > 0 && named.size === words.size && [...words].every((w) => named.has(w))
    },
    (candidate) => {
      const named = nameWords(candidate)
      return words.size > 0 && [...words].every((word) => named.has(word))
    },
  ]
  for (const close of closeness) {
    const matches = pool.filter((candidate) => close(candidate.name))
    if (matches.length === 0) continue
    const elements = distinct(matches)
    const [only] = elements
    return elements.length === 1 && only != null
      ? { kind: 'one', element: only }
      : { kind: 'several', refs: elements.map(refOf) }
  }
  return { kind: 'none' }
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
  for (const [ref, element] of referenced) {
    if (element.deref()?.isConnected !== true) referenced.delete(ref)
  }
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
      clean(shownText(cell), 30),
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
      if (skippedTags.has(child.tagName) || child.matches(notPage) || child.matches(secret)) {
        continue
      }
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
            `${clean(shownText(term), 40)}: ${clean(shownText(term.nextElementSibling), 80)}`,
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
