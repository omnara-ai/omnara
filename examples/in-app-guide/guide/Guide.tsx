'use client'

import { Fragment, useEffect, useRef, useState, useSyncExternalStore, type CSSProperties, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { runTool, trackRequests } from './actions'
import { Overlay } from './overlay'
import { GuideSession, type Conversation, type GuideState, type Mode } from './session'
import { takeSnapshot } from './snapshot'
import { guideCss } from './styles'

export type GuideProps = {
  /** The host app's guide route (see createGuideHandler). */
  endpoint?: string
  /** Launcher and panel title, e.g. "Ask Acme". */
  title?: string
  accent?: string
  /** Example questions shown in an empty conversation. */
  suggestions?: string[]
  /** Which bottom corner the launcher and panel sit in. */
  placement?: 'bottom-right' | 'bottom-left'
  /** Distance from that corner in pixels, e.g. to sit beside a support chat bubble. */
  offset?: { side?: number; bottom?: number }
  /**
   * Hide "Do it for me": the guide only points and the user does every click. Set it
   * when the agent config was rendered with "do_mode": false, which also removes the
   * agent's tools for acting on the page.
   */
  showOnly?: boolean
}

const OPEN_EVENT = 'omnara-guide:open'

const SHIELDED_EVENTS = ['pointerdown', 'mousedown', 'touchstart', 'click', 'focusin', 'focusout', 'keydown', 'keypress', 'keyup']
const HIDING_ATTRIBUTES = ['aria-hidden', 'inert', 'data-aria-hidden']

/**
 * Keeps the guide usable while the host app has a modal dialog open. Dialog libraries
 * treat everything outside the dialog as "outside": they hide it from assistive tech,
 * pull keyboard focus back into the dialog, and close the dialog when it is clicked.
 * The guide sits outside, so a step inside a dialog would lose its Next button and
 * typing in the panel would be impossible. Two measures:
 * - events that start in the guide stop at its host element, so the app's
 *   document-level listeners (click-outside, focus traps, keyboard shortcuts) never
 *   see them. The guide's own handlers sit inside the shadow root and run first.
 * - attributes that hide the host are removed as soon as the app adds them.
 * - when focus moves from a dialog into the guide, the dialog's focus trap is not told,
 *   so it doesn't pull focus straight back. Only that one blur is withheld from the app.
 */
function shieldFromHost(host: HTMLElement) {
  const stop = (e: Event) => e.stopPropagation()
  for (const type of SHIELDED_EVENTS) host.addEventListener(type, stop)
  const keepFocus = (e: FocusEvent) => {
    if (e.relatedTarget !== host) return
    if ((e.target as Element | null)?.closest?.('[role="dialog"],[role="alertdialog"],dialog')) e.stopImmediatePropagation()
  }
  window.addEventListener('focusout', keepFocus, true)
  window.addEventListener('blur', keepFocus, true)
  const unhide = () => {
    for (const attr of HIDING_ATTRIBUTES) if (host.hasAttribute(attr)) host.removeAttribute(attr)
  }
  const observer = new MutationObserver(unhide)
  observer.observe(host, { attributes: true, attributeFilter: HIDING_ATTRIBUTES })
  return () => {
    observer.disconnect()
    for (const type of SHIELDED_EVENTS) host.removeEventListener(type, stop)
    window.removeEventListener('focusout', keepFocus, true)
    window.removeEventListener('blur', keepFocus, true)
  }
}

/** Open the guide from the host app's own UI, e.g. a "Help" item in its navigation. */
export function openGuide() {
  window.dispatchEvent(new CustomEvent(OPEN_EVENT))
}

/**
 * The in-app guide: a launcher, a chat panel, and the cursor overlay. Mount it once in
 * the app's root layout, for signed-in users only. It renders into its own shadow root,
 * so the host's CSS can't restyle it and its CSS can't leak into the host.
 */
export function Guide({ endpoint = '/api/guide', title = 'Ask for help', accent = '#4f46e5', suggestions = [], placement = 'bottom-right', offset, showOnly = false }: GuideProps) {
  const [mount, setMount] = useState<{ container: HTMLElement; session: GuideSession } | null>(null)

  useEffect(() => {
    const host = document.createElement('div')
    host.setAttribute('data-omnara-guide', '')
    // Match the host app's typeface; the shadow root doesn't inherit it otherwise.
    host.style.setProperty('--g-font', getComputedStyle(document.body).fontFamily)
    const shadow = host.attachShadow({ mode: 'open' })
    const style = document.createElement('style')
    style.textContent = guideCss(accent)
    const layer = document.createElement('div')
    layer.className = 'g-layer'
    const container = document.createElement('div')
    shadow.append(style, layer, container)
    document.body.append(host)
    const unshield = shieldFromHost(host)

    trackRequests(endpoint)
    const overlay = new Overlay(layer)
    const session = new GuideSession(endpoint, overlay, host)
    setMount({ container, session })
    void session.init()

    // Development only: lets the browser smoke test drive the tools without a model.
    const debug = process.env.NODE_ENV !== 'production'
    if (debug) {
      ;(window as unknown as Record<string, unknown>).__omnaraGuide = {
        snapshot: () => takeSnapshot(host).text,
        highlighted: () => overlay.current,
        state: () => session.getState(),
        run: (name: string, input: Record<string, unknown>, signal = new AbortController().signal) =>
          runTool({ id: 'debug', name, input }, { overlay, host, signal }),
      }
    }
    return () => {
      if (debug) delete (window as unknown as Record<string, unknown>).__omnaraGuide
      session.destroy()
      overlay.destroy()
      unshield()
      host.remove()
    }
  }, [endpoint, accent])

  if (!mount) return null
  return createPortal(<Panel session={mount.session} title={title} suggestions={suggestions} placement={placement} offset={offset} showOnly={showOnly} />, mount.container)
}

const STATUS: Record<GuideState['status'], string> = {
  idle: '',
  thinking: 'Thinking…',
  acting: 'Working on it…',
  waiting_user: 'Your turn: follow the highlight on the page',
  waiting_approval: 'Waiting for your approval',
}

function Panel({ session, title, suggestions, placement, offset, showOnly }: { session: GuideSession; title: string; suggestions: string[]; placement: NonNullable<GuideProps['placement']>; offset?: GuideProps['offset']; showOnly: boolean }) {
  const state = useSyncExternalStore(session.subscribe, session.getState, session.getState)
  // A browser that chose "Do it for me" before the app became show-only goes back to showing.
  useEffect(() => {
    if (showOnly && state.mode !== 'guide') session.setMode('guide')
  }, [showOnly, state.mode, session])
  const [open, setOpen] = useState(() => readFlag('omnara-guide-open'))
  const [draft, setDraft] = useState('')
  // While the guide is pointing at the page, the panel shrinks to a pill so it can't
  // cover what it is pointing at. The user can bring the conversation back.
  const [expanded, setExpanded] = useState(false)
  // Once a walkthrough has started, the panel stays a pill while the agent thinks about
  // the next step, so it doesn't pop open over the page between steps. It comes back
  // when the turn ends or an approval is needed.
  const [walking, setWalking] = useState(false)
  useEffect(() => {
    if (state.status === 'waiting_user' || state.status === 'acting') setWalking(true)
    else if (state.status !== 'thinking') setWalking(false)
  }, [state.status])
  const busy = state.status === 'waiting_user' || state.status === 'acting' || (walking && state.status === 'thinking')
  const compact = open && busy && !expanded
  // Earlier conversations: null while the list is closed.
  const [history, setHistory] = useState<{ items: Conversation[] | null; error?: string } | null>(null)
  const body = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLTextAreaElement>(null)

  const showHistory = () => {
    setHistory({ items: null })
    session.conversations().then(
      (items) => setHistory((h) => h && { items }),
      (err: Error) => setHistory((h) => h && { items: [], error: err.message }),
    )
  }

  useEffect(() => writeFlag('omnara-guide-open', open), [open])
  useEffect(() => {
    if (!busy) setExpanded(false)
  }, [busy])
  useEffect(() => {
    const onOpen = () => {
      setOpen(true)
      setExpanded(true)
    }
    window.addEventListener(OPEN_EVENT, onOpen)
    return () => window.removeEventListener(OPEN_EVENT, onOpen)
  }, [])
  useEffect(() => {
    body.current?.scrollTo({ top: body.current.scrollHeight, behavior: 'smooth' })
  }, [state.messages.length, state.preview, state.approvals.length, state.status])
  useEffect(() => {
    if (open) input.current?.focus()
  }, [open])

  const send = (text: string) => {
    if (!text.trim()) return
    void session.send(text)
    setDraft('')
  }

  return (
    <div
      className="g-root"
      data-theme="light"
      data-placement={placement}
      style={{ '--g-side': `${offset?.side ?? 20}px`, '--g-bottom': `${offset?.bottom ?? 20}px` } as CSSProperties}
    >
      {compact && (
        <div className="g-pill" role="status">
          <span className="g-dots" aria-hidden="true">
            <i />
            <i />
            <i />
          </span>
          <span className="g-grow">{state.status === 'waiting_user' ? 'Your turn: follow the highlight' : 'Working on it…'}</span>
          <button type="button" className="g-btn" onClick={() => setExpanded(true)}>
            Show chat
          </button>
        </div>
      )}
      {open && !compact && (
        <section className="g-panel" aria-label={title}>
          <div className="g-head">
            <div className="g-grow">
              <div className="g-title">{title}</div>
              <div className="g-sub">Answers from the docs, shown right on this page</div>
            </div>
            <button className="g-icon" type="button" title="Earlier conversations" aria-label="Earlier conversations" aria-pressed={!!history} onClick={() => (history ? setHistory(null) : showHistory())}>
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
                <circle cx="8" cy="8" r="5.8" />
                <path d="M8 4.8V8l2.2 1.4" />
              </svg>
            </button>
            <button
              className="g-icon"
              type="button"
              title="New conversation"
              aria-label="New conversation"
              onClick={() => {
                setHistory(null)
                void session.reset()
              }}
            >
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
                <path d="M8 3v10M3 8h10" />
              </svg>
            </button>
            <button className="g-icon" type="button" title="Close" aria-label="Close guide" onClick={() => setOpen(false)}>
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
                <path d="M4 4l8 8M12 4l-8 8" />
              </svg>
            </button>
          </div>

          {history && (
            <div className="g-history">
              <div className="g-history-title">Earlier conversations</div>
              {history.items === null && <div className="g-empty">Loading…</div>}
              {history.error && <div className="g-msg g-notice">{history.error}</div>}
              {history.items?.length === 0 && !history.error && <div className="g-empty">Nothing yet. Your conversations will be listed here.</div>}
              {history.items?.map((c) => (
                <button
                  key={c.session}
                  type="button"
                  className="g-history-item"
                  aria-current={c.current ? 'true' : undefined}
                  onClick={() => {
                    setHistory(null)
                    void session.open(c.session)
                  }}
                >
                  <span className="g-history-name">{c.title}</span>
                  <span className="g-history-when">{c.current ? 'Open now · ' : ''}{timeAgo(c.updatedAt)}</span>
                </button>
              ))}
            </div>
          )}

          <div className="g-modebar" hidden={!!history || showOnly}>
            <div className="g-seg" role="group" aria-label="Guide mode">
              {(['guide', 'do'] as Mode[]).map((m) => (
                <button key={m} type="button" aria-pressed={state.mode === m} onClick={() => session.setMode(m)}>
                  {m === 'guide' ? 'Show me' : 'Do it for me'}
                </button>
              ))}
            </div>
            <span className="g-modehint">{state.mode === 'guide' ? 'You click, I point' : 'I click, you approve'}</span>
          </div>

          <div className="g-body" ref={body} hidden={!!history}>
            {state.ready && state.messages.length === 0 && (
              <div className="g-empty">
                <strong>How can I help?</strong> Ask how to do something and I'll walk you through it on this page.
                {suggestions.length > 0 && (
                  <div className="g-chips">
                    {suggestions.map((s) => (
                      <button key={s} type="button" className="g-chip" onClick={() => send(s)}>
                        {s}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            )}
            {state.messages.map((m) => (
              <div key={m.id} className={`g-msg g-${m.role}`}>
                {renderText(m.text)}
              </div>
            ))}
            {state.preview && <div className="g-msg g-assistant g-preview">{renderText(state.preview)}</div>}
            {state.approvals.map((a) => (
              <div key={a.id} className="g-approval" role="group" aria-label="Approval needed">
                <h4>{a.title}</h4>
                {a.summary && <p>{a.summary}</p>}
                <div className="g-row">
                  <button type="button" className="g-btn g-btn-primary" onClick={() => void session.resolveApproval(a, true)}>
                    Approve
                  </button>
                  <button type="button" className="g-btn" onClick={() => void session.resolveApproval(a, false)}>
                    Don't do it
                  </button>
                </div>
              </div>
            ))}
            {state.status !== 'idle' && state.approvals.length === 0 && (
              <div className="g-status" role="status">
                <span className="g-dots" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                </span>
                {STATUS[state.status]}
              </div>
            )}
          </div>

          <form
            hidden={!!history}
            className="g-compose"
            onSubmit={(e) => {
              e.preventDefault()
              send(draft)
            }}
          >
            <textarea
              ref={input}
              rows={1}
              value={draft}
              placeholder="How do I…"
              aria-label="Ask a question"
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  send(draft)
                }
              }}
            />
            <button className="g-send" type="submit" aria-label="Send" disabled={!draft.trim()}>
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                <path d="M8 13V3M3.5 7.5L8 3l4.5 4.5" />
              </svg>
            </button>
          </form>
          <div className="g-foot">Built on Omnara</div>
        </section>
      )}

      <button className="g-launcher" type="button" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <svg viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
          <path d="M10 1.5l1.6 4.4 4.4 1.6-4.4 1.6L10 13.5 8.4 9.1 4 7.5l4.4-1.6L10 1.5zM15.5 12l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8.8-2.2z" />
        </svg>
        {open ? 'Close' : title}
      </button>
    </div>
  )
}

/** Paragraphs, bullets, **bold**, `code` and [links](https://…); nothing else. */
function renderText(text: string): ReactNode {
  return text.split(/\n+/).map((line, i) => {
    const bullet = /^\s*[-*•]\s+/.test(line)
    const content = line.replace(/^\s*[-*•]\s+/, '')
    const parts: ReactNode[] = []
    const re = /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|\*\*([^*]+)\*\*|`([^`]+)`/g
    let last = 0
    for (let m = re.exec(content); m; m = re.exec(content)) {
      if (m.index > last) parts.push(content.slice(last, m.index))
      if (m[1] && m[2]) {
        parts.push(
          <a key={m.index} href={m[2]} target="_blank" rel="noreferrer">
            {m[1]}
          </a>,
        )
      } else if (m[3]) parts.push(<strong key={m.index}>{m[3]}</strong>)
      else if (m[4]) parts.push(<code key={m.index}>{m[4]}</code>)
      last = m.index + m[0].length
    }
    if (last < content.length) parts.push(content.slice(last))
    return <p key={i}>{bullet ? <Fragment>• {parts}</Fragment> : parts}</p>
  })
}

function timeAgo(iso: string) {
  const minutes = Math.round((Date.now() - new Date(iso).getTime()) / 60000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min ago`
  if (minutes < 60 * 24) return `${Math.round(minutes / 60)} h ago`
  if (minutes < 60 * 24 * 7) return `${Math.round(minutes / 1440)} d ago`
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function readFlag(key: string) {
  try {
    return localStorage.getItem(key) === '1'
  } catch {
    return false
  }
}
function writeFlag(key: string, value: boolean) {
  try {
    localStorage.setItem(key, value ? '1' : '0')
  } catch {}
}
