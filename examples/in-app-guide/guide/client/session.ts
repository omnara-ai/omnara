// One guide conversation in the browser. It talks only to the host's guide route (never
// to Omnara directly), follows the agent's event stream, runs the agent's custom tools in
// this page, and surfaces approvals for submit steps.
//
// The conversation is identified by a signed session token the server hands back on the
// first message. It travels in a header rather than a cookie, so the same widget works
// same-origin (inside the host app) and cross-origin (an embed pointing at another host).

import { runTool, type ToolCall } from './actions'
import { sleep, type Overlay } from './overlay'
import { resolveRef, takeSnapshot } from './snapshot'

export type Mode = 'guide' | 'do'
export type Message = { id: string; role: 'user' | 'assistant' | 'notice'; text: string }
export type Approval = { id: string; toolCallId: string; title: string; summary: string; context: { label: string; value: string }[] }
export type Status = 'idle' | 'thinking' | 'acting' | 'waiting_user' | 'waiting_approval'

export type Conversation = { session: string; title: string; updatedAt: string; current: boolean }

export type GuideState = {
  ready: boolean
  status: Status
  mode: Mode
  messages: Message[]
  preview: string
  approvals: Approval[]
}

const ACTIVE_TOOLS = new Set(['show_step', 'click', 'fill', 'submit'])

export class GuideSession {
  private state: GuideState = { ready: false, status: 'idle', mode: 'guide', messages: [], preview: '', approvals: [] }
  private listeners = new Set<() => void>()
  private streamAbort: AbortController | null = null
  private lastSequence = 0
  private previews = new Map<string, string>()
  private handled = new Set<string>()
  private running: { id: string; abort: AbortController } | null = null
  private draining = false
  private poll = 0
  private highlightedApproval = ''
  private answered = new Set<string>()
  // A conversation reopened from history: anything it was waiting on is stale, so it is
  // shown read-only until the user sends a new message.
  private reopened = false
  private token: string | null = null
  private readonly tokenKey: string

  constructor(
    private endpoint: string,
    private overlay: Overlay,
    private host: Element,
  ) {
    this.tokenKey = `omnara-guide-session:${endpoint}`
    try {
      const saved = localStorage.getItem('omnara-guide-mode')
      if (saved === 'do' || saved === 'guide') this.state.mode = saved
      this.token = localStorage.getItem(this.tokenKey)
    } catch {}
  }

  subscribe = (fn: () => void) => {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }
  getState = () => this.state

  private set(patch: Partial<GuideState>) {
    this.state = { ...this.state, ...patch }
    for (const fn of this.listeners) fn()
  }

  private setToken(token: string | null) {
    this.token = token
    try {
      if (token) localStorage.setItem(this.tokenKey, token)
      else localStorage.removeItem(this.tokenKey)
    } catch {}
  }

  private api(path: string, init: RequestInit = {}) {
    const headers: Record<string, string> = { ...(init.headers as Record<string, string>) }
    if (this.token) headers['X-Guide-Session'] = this.token
    if (init.body) headers['Content-Type'] = 'application/json'
    // Default credentials: the host's own cookies go along same-origin, none cross-origin.
    return fetch(`${this.endpoint}${path}`, { ...init, headers })
  }

  setMode(mode: Mode) {
    try {
      localStorage.setItem('omnara-guide-mode', mode)
    } catch {}
    this.set({ mode })
  }

  /** Load an existing conversation (after a reload) and reconnect to it. */
  async init() {
    try {
      if (!this.token) return
      const res = await this.api('/history')
      if (res.status === 404 || res.status === 401) return this.setToken(null)
      if (res.ok) {
        const h = (await res.json()) as { messages: { role: 'user' | 'assistant'; text: string }[]; lastSequence: number }
        this.lastSequence = h.lastSequence
        this.set({ messages: h.messages.map((m, i) => ({ id: `h${i}`, ...m })) })
        this.connect()
        await this.reconcile()
      }
    } catch {
      // The guide stays usable; the next message starts a fresh conversation if needed.
    } finally {
      this.set({ ready: true })
    }
  }

  /** This user's recent conversations, newest first. */
  async conversations(): Promise<Conversation[]> {
    const res = await this.api('/conversations')
    if (!res.ok) throw new Error('Could not load your conversations.')
    return ((await res.json()) as { data: Conversation[] }).data
  }

  /** Switch to an earlier conversation. It can be continued by sending a message. */
  async open(session: string) {
    if (session === this.token && !this.reopened && this.state.messages.length) return
    this.detach()
    this.setToken(session)
    this.reopened = true
    this.set({ ready: false, status: 'idle', messages: [], preview: '', approvals: [] })
    try {
      const res = await this.api('/history')
      if (!res.ok) throw new Error('Could not load that conversation.')
      const h = (await res.json()) as { messages: { role: 'user' | 'assistant'; text: string }[]; lastSequence: number }
      this.lastSequence = h.lastSequence
      this.set({ messages: h.messages.map((m, i) => ({ id: `h${i}`, ...m })) })
    } catch (err) {
      this.notice(err instanceof Error ? err.message : 'Could not load that conversation.')
    } finally {
      this.set({ ready: true })
    }
  }

  /** Whatever a reopened conversation was waiting on happened on an older page: end it. */
  private async releaseStale() {
    const res = await this.api('/tool-calls?state=ready')
    if (!res.ok) return
    const { data } = (await res.json()) as { data: ToolCall[] }
    for (const call of data) {
      this.handled.add(call.id)
      await this.api(`/tool-calls/${call.id}/result`, {
        method: 'POST',
        body: JSON.stringify({ outcome: 'succeeded', text: 'outcome: interrupted\nThe user left this conversation and has come back to it later. This step was not done and the page may be different now. Their new message is what to answer.' }),
      })
    }
  }

  private detach() {
    this.running?.abort.abort()
    this.streamAbort?.abort()
    this.streamAbort = null
    clearInterval(this.poll)
    this.overlay.clearHighlight()
    this.overlay.hideCursor()
    this.handled.clear()
    this.previews.clear()
    this.lastSequence = 0
  }

  async send(text: string) {
    text = text.trim()
    if (!text) return
    const reopened = this.reopened
    const busy = this.state.status !== 'idle' || reopened
    const context = `Mode: ${this.state.mode}\nCurrent page:\n${takeSnapshot(this.host).text}`
    this.set({
      messages: [...this.state.messages, { id: crypto.randomUUID(), role: 'user', text }],
      status: 'thinking',
      approvals: [],
    })
    try {
      const res = await this.api('/message', {
        method: 'POST',
        body: JSON.stringify({ text, context, key: crypto.randomUUID(), steering: busy }),
      })
      const body = (await res.json().catch(() => ({}))) as { error?: string; session?: string }
      if (!res.ok) throw new Error(body.error ?? `HTTP ${res.status}`)
      if (body.session) this.setToken(body.session)
      // The message goes in first, so the agent reads it together with the news that its
      // old step was dropped, and answers once.
      if (reopened) {
        this.reopened = false
        await this.releaseStale()
      }
      // A step waiting on the user is now superseded by this message.
      this.running?.abort.abort()
      this.overlay.clearHighlight()
      this.connect()
    } catch (err) {
      this.notice(err instanceof Error ? err.message : 'Could not reach the guide.')
      this.set({ status: 'idle' })
    }
  }

  async resolveApproval(approval: Approval, allow: boolean) {
    // Remember it, so a refresh that races with the answer can't show the card again.
    this.answered.add(approval.id)
    this.set({ approvals: this.state.approvals.filter((a) => a.id !== approval.id), status: 'acting' })
    this.overlay.clearHighlight()
    await this.api(`/interactions/${approval.id}/resolve`, {
      method: 'POST',
      body: JSON.stringify({ answers: [{ option_indices: [allow ? 0 : 1] }] }),
    })
    if (!allow) this.set({ status: 'thinking' })
  }

  /** Start a new conversation. The old one stays in the history list. */
  async reset() {
    this.detach()
    this.reopened = false
    this.setToken(null)
    this.set({ status: 'idle', messages: [], preview: '', approvals: [] })
  }

  destroy() {
    this.streamAbort?.abort()
    clearInterval(this.poll)
    this.running?.abort.abort()
  }

  private notice(text: string) {
    this.set({ messages: [...this.state.messages, { id: crypto.randomUUID(), role: 'notice', text }] })
  }

  /** Follow the agent's event stream (SSE over fetch, so it can carry the session header). */
  private connect() {
    if (this.streamAbort) return
    const abort = new AbortController()
    this.streamAbort = abort
    void (async () => {
      let backoff = 500
      while (!abort.signal.aborted) {
        try {
          const res = await this.api('/stream', {
            headers: { Accept: 'text/event-stream', 'Last-Event-ID': String(this.lastSequence) },
            signal: abort.signal,
          })
          if (!res.ok || !res.body) throw new Error(`stream HTTP ${res.status}`)
          backoff = 500
          const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
          let buffer = ''
          for (;;) {
            const { value, done } = await reader.read()
            if (done) break
            buffer += value.replace(/\r\n/g, '\n')
            for (let i = buffer.indexOf('\n\n'); i >= 0; i = buffer.indexOf('\n\n')) {
              const stop = this.onFrame(buffer.slice(0, i))
              buffer = buffer.slice(i + 2)
              if (stop) return abort.abort()
            }
          }
        } catch {
          if (abort.signal.aborted) return
        }
        // Reconnect; the server replays saved events after Last-Event-ID.
        await sleep(backoff)
        backoff = Math.min(backoff * 2, 8000)
      }
    })().finally(() => {
      if (this.streamAbort === abort) this.streamAbort = null
    })

    // Stream frames are hints; reconcile now and then in case one was missed.
    clearInterval(this.poll)
    this.poll = window.setInterval(() => {
      if (this.state.status !== 'idle') void this.reconcile()
    }, 4000)
  }

  /** Handle one SSE frame. Returns true when the stream should stop for good. */
  private onFrame(frame: string): boolean {
    let event = 'message'
    let data = ''
    for (const line of frame.split('\n')) {
      if (!line || line.startsWith(':')) continue
      const colon = line.indexOf(':')
      const field = colon < 0 ? line : line.slice(0, colon)
      const value = colon < 0 ? '' : line.slice(colon + 1).replace(/^ /, '')
      if (field === 'event') event = value
      else if (field === 'data') data += data ? `\n${value}` : value
    }
    if (!data) return false

    if (event === 'model_output_delta') {
      const d = JSON.parse(data) as { model_call_context_id: string; event?: { kind: string; delta?: string } }
      if (d.event?.kind !== 'text_delta' || !d.event.delta) return false
      this.previews.set(d.model_call_context_id, (this.previews.get(d.model_call_context_id) ?? '') + d.event.delta)
      this.set({ preview: [...this.previews.values()].join('\n'), status: this.state.status === 'idle' ? 'thinking' : this.state.status })
    } else if (event === 'model_output') {
      const ev = JSON.parse(data) as { sequence: number; model_call_context_id: string; stop_reason: string; content_blocks: { type: string; text?: string }[] }
      if (ev.sequence <= this.lastSequence) return false
      this.lastSequence = ev.sequence
      this.previews.delete(ev.model_call_context_id)
      const text = ev.content_blocks.filter((b) => b.type === 'text' && b.text).map((b) => b.text).join('\n').trim()
      const error = ev.content_blocks.find((b) => b.type === 'error')?.text
      const usesTools = ev.content_blocks.some((b) => b.type === 'tool_call')
      const messages = [...this.state.messages]
      // Text sent alongside a tool call is narration; the step caption already says it.
      if (text && !usesTools) messages.push({ id: `m${ev.sequence}`, role: 'assistant', text })
      if (error) messages.push({ id: `x${ev.sequence}`, role: 'notice', text: error })
      const done = !usesTools && ev.stop_reason !== 'max_tokens'
      this.set({ messages, preview: [...this.previews.values()].join('\n'), status: done ? 'idle' : this.state.status })
      // The turn is over: take the highlight and the virtual cursor off the page.
      if (done) {
        this.overlay.clearHighlight()
        this.overlay.hideCursor()
      }
    } else if (event === 'tool_call_update') {
      void this.reconcile()
    } else if (event === 'error') {
      const err = JSON.parse(data) as { code?: string; message?: string }
      if (err.code === 'service_unavailable') return false
      this.notice(err.message ?? 'The guide disconnected.')
      this.set({ status: 'idle' })
      this.overlay.clearHighlight()
      this.overlay.hideCursor()
      return true
    }
    return false
  }

  private async reconcile() {
    await Promise.all([this.drainToolCalls(), this.refreshApprovals()]).catch(() => {})
  }

  /** Run ready custom tool calls one at a time, in order. */
  private async drainToolCalls() {
    if (this.draining) return
    this.draining = true
    try {
      for (;;) {
        const res = await this.api('/tool-calls?state=ready')
        if (!res.ok) return
        const { data } = (await res.json()) as { data: ToolCall[] }
        const next = data.find((c) => !this.handled.has(c.id) && ACTIVE_TOOLS.has(c.name))
        if (!next) return
        this.handled.add(next.id)
        const abort = new AbortController()
        this.running = { id: next.id, abort }
        this.set({ status: next.name === 'show_step' ? 'waiting_user' : 'acting' })
        const result = await runTool(next, { overlay: this.overlay, host: this.host, signal: abort.signal })
        this.running = null
        this.set({ status: 'thinking' })
        await this.api(`/tool-calls/${next.id}/result`, { method: 'POST', body: JSON.stringify(result) })
      }
    } finally {
      this.draining = false
    }
  }

  private async refreshApprovals() {
    const res = await this.api('/interactions')
    if (!res.ok) return
    const { data } = (await res.json()) as {
      data: { id: string; tool_call_id: string; tool_name?: string; request: { title: string; context?: { label: string; value: string }[] } }[]
    }
    const open = data.filter((i) => !this.answered.has(i.id))
    if (!open.length) {
      if (this.state.approvals.length) this.set({ approvals: [] })
      return
    }
    const pending = await this.api('/tool-calls?state=awaiting_permission')
      .then((r) => (r.ok ? (r.json() as Promise<{ data: ToolCall[] }>) : { data: [] }))
      .catch(() => ({ data: [] as ToolCall[] }))
    // The user may have answered while that request was in flight: don't bring the card back.
    const approvals = open.filter((i) => !this.answered.has(i.id)).map((i) => {
      const call = pending.data.find((c) => c.id === i.tool_call_id)
      return {
        id: i.id,
        toolCallId: i.tool_call_id,
        title: call ? String(call.input.text ?? i.request.title) : i.request.title,
        summary: call ? String(call.input.summary ?? '') : '',
        context: i.request.context ?? [],
      }
    })
    if (!approvals.length) return
    this.set({ approvals, status: 'waiting_approval' })

    // Point at the button that will be clicked once the user approves.
    const first = approvals[0]
    const call = first && pending.data.find((c) => c.id === first.toolCallId)
    const target = call ? resolveRef(String(call.input.ref ?? '')) : null
    if (first && target && this.highlightedApproval !== first.id) {
      this.highlightedApproval = first.id
      await this.overlay.moveTo(target)
      this.overlay.highlight(target, 'Approve this in the guide panel and I will click it.')
    }
  }
}
