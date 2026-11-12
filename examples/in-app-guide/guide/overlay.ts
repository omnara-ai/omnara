// The visible guide layer: a virtual cursor, a highlight ring that tracks its target
// through scrolling and layout changes, and a caption with the step text. It lives in
// the guide's shadow root and never intercepts clicks meant for the page.

export type CaptionAction = { label: string; primary?: boolean; onClick: () => void }

const CURSOR_SVG = `<svg width="26" height="30" viewBox="0 0 26 30" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
<path d="M2.5 2.2 L2.5 23.5 L8.4 18.1 L12.4 27.3 L16.6 25.5 L12.7 16.5 L20.6 16.2 Z" fill="var(--g-accent)" stroke="white" stroke-width="2" stroke-linejoin="round"/></svg>`

export class Overlay {
  private cursor: HTMLDivElement
  private ring: HTMLDivElement
  private caption: HTMLDivElement
  private target: Element | null = null
  private frame = 0
  private cursorPos = { x: innerWidth - 80, y: innerHeight - 120 }

  constructor(root: HTMLElement) {
    this.cursor = el('div', 'g-cursor')
    this.cursor.innerHTML = CURSOR_SVG
    this.ring = el('div', 'g-ring')
    this.caption = el('div', 'g-caption')
    this.caption.setAttribute('role', 'status')
    root.append(this.ring, this.caption, this.cursor)
    this.placeCursor(this.cursorPos.x, this.cursorPos.y, false)
  }

  /** The element currently highlighted, if any. */
  get current(): Element | null {
    return this.target
  }

  /** Scroll the element into view if needed, then glide the cursor onto it. */
  async moveTo(target: Element): Promise<void> {
    target = visualTarget(target)
    const rect = target.getBoundingClientRect()
    if (rect.top < 60 || rect.bottom > innerHeight - 60) {
      target.scrollIntoView({ block: 'center', behavior: 'smooth' })
      await sleep(420)
    }
    const r = target.getBoundingClientRect()
    this.cursor.classList.add('g-visible')
    this.placeCursor(r.left + Math.min(r.width / 2, 48), r.top + r.height / 2, true)
    await sleep(650)
  }

  /** Ring the element and show the caption until clearHighlight() is called. */
  highlight(target: Element, text: string, actions: CaptionAction[] = []) {
    this.target = visualTarget(target)
    this.caption.replaceChildren()
    const p = el('p', 'g-caption-text')
    p.textContent = text
    this.caption.append(p)
    if (actions.length) {
      const row = el('div', 'g-caption-actions')
      for (const a of actions) {
        const b = el('button', a.primary ? 'g-btn g-btn-primary' : 'g-btn')
        b.type = 'button'
        b.textContent = a.label
        b.addEventListener('click', a.onClick)
        row.append(b)
      }
      this.caption.append(row)
    }
    this.ring.classList.add('g-visible')
    this.caption.classList.add('g-visible')
    cancelAnimationFrame(this.frame)
    const track = () => {
      if (!this.target) return
      this.position(this.target)
      this.frame = requestAnimationFrame(track)
    }
    track()
  }

  clearHighlight() {
    this.target = null
    cancelAnimationFrame(this.frame)
    this.ring.classList.remove('g-visible')
    this.caption.classList.remove('g-visible')
  }

  /** A press animation at the cursor, for clicks the guide performs itself. */
  async press() {
    this.cursor.classList.add('g-press')
    await sleep(180)
    this.cursor.classList.remove('g-press')
  }

  hideCursor() {
    this.cursor.classList.remove('g-visible')
  }

  destroy() {
    cancelAnimationFrame(this.frame)
    this.cursor.remove()
    this.ring.remove()
    this.caption.remove()
  }

  private position(target: Element) {
    const r = target.getBoundingClientRect()
    const pad = 4
    Object.assign(this.ring.style, {
      transform: `translate(${r.left - pad}px, ${r.top - pad}px)`,
      width: `${r.width + pad * 2}px`,
      height: `${r.height + pad * 2}px`,
    })
    // Keep the cursor on the target while it moves (scrolling, animations).
    this.placeCursor(r.left + Math.min(r.width / 2, 48), r.top + r.height / 2, false)

    const cw = this.caption.offsetWidth
    const ch = this.caption.offsetHeight
    const clampTop = (y: number) => Math.min(Math.max(8, y), innerHeight - ch - 8)
    let top: number
    let left: number
    // Inside a dialog, sit beside it so the caption never covers the choices.
    const dialog = target.closest('dialog,[role="dialog"],[role="alertdialog"]')
    const d = dialog?.getBoundingClientRect()
    if (d && d.left - cw - 16 >= 8) {
      left = d.left - cw - 16
      top = clampTop(r.top + r.height / 2 - ch / 2)
    } else if (target.matches('[role="combobox"],[aria-haspopup],select')) {
      // This opens a list or menu underneath, so stay out of its way: above, or beside.
      if (r.top - ch - 14 >= 8) {
        top = r.top - ch - 14
        left = Math.min(Math.max(8, r.left), innerWidth - cw - 8)
      } else {
        top = clampTop(r.top)
        left = r.right + 14 + cw < innerWidth ? r.right + 14 : Math.max(8, r.left - cw - 14)
      }
    } else {
      const below = r.bottom + 14 + ch < innerHeight
      top = below ? r.bottom + 14 : Math.max(8, r.top - ch - 14)
      left = Math.min(Math.max(8, r.left), innerWidth - cw - 8)
    }
    this.caption.style.transform = `translate(${left}px, ${top}px)`
  }

  private placeCursor(x: number, y: number, animate: boolean) {
    this.cursorPos = { x, y }
    this.cursor.classList.toggle('g-animate', animate)
    this.cursor.style.transform = `translate(${x - 3}px, ${y - 2}px)`
  }
}

/** Radios and checkboxes are tiny; point at the label the user actually reads. */
function visualTarget(target: Element): Element {
  if (target instanceof HTMLInputElement && (target.type === 'radio' || target.type === 'checkbox')) {
    const label = target.closest('label') ?? (target.id ? document.querySelector(`label[for="${CSS.escape(target.id)}"]`) : null)
    if (label) return label
  }
  return target
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className: string): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag)
  node.className = className
  return node
}

export const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))
