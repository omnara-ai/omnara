// Server half of the in-app guide: a framework-agnostic route handler that keeps the
// Omnara API key on the server and relays the widget's calls to Omnara. Mount it as a
// catch-all route, e.g. app/api/guide/[...path]/route.ts:
//
//   export const { GET, POST } = createGuideHandler({ ... })
//
// On Omnara, every user of the host app gets their own agent profile, created the first
// time they use the guide and named after them with a short tag from their user ID
// ("Acme guide · Dana Whitfield · 3f9a2c1b"). The profile's config is the guide's config
// with that user's name and role written into the instruction. Each conversation is one
// agent launched from the user's profile, so a user's history is simply the agents of
// their profile.
//
// The host app decides who the user is (getUser). The conversation is identified by a
// session token (HMAC-signed user ID + agent ID) that the widget sends in a header, so a
// browser can only reach its own conversation. Serve this route from the app's own origin.

export type GuideUser = {
  /** Stable user ID in the host app. */
  id: string
  displayName?: string
  /** One or two lines the agent should know about this user, e.g. role and workspace. */
  context: string
}

export type GuideHandlerOptions = {
  token: string
  orgId: string
  projectId: string
  /**
   * The guide's agent config (YAML). Its instruction must contain a line with `{{user}}`,
   * which is replaced by each user's name and context when their profile is made.
   */
  config: string
  /** Start of every profile name, e.g. "Acme guide". */
  profilePrefix?: string
  getUser: (req: Request) => GuideUser | null | Promise<GuideUser | null>
  apiUrl?: string
  basePath?: string
  /** Signs session tokens. Defaults to the API key. */
  sessionSecret?: string
}

type Json = Record<string, unknown>

const ID = /^[a-z]{2,6}_[a-z0-9]{10,40}$/

class OmnaraError extends Error {
  constructor(
    readonly status: number,
    readonly body: string,
  ) {
    super(`Omnara API ${status}: ${body.slice(0, 300)}`)
  }
}

export function createGuideHandler(opts: GuideHandlerOptions) {
  const apiUrl = (opts.apiUrl ?? 'https://api.omnara.com/v1').replace(/\/$/, '')
  const basePath = (opts.basePath ?? '/api/guide').replace(/\/$/, '')
  const projectBase = `${apiUrl}/orgs/${opts.orgId}/projects/${opts.projectId}`

  async function omnara(path: string, init: RequestInit & { idempotencyKey?: string } = {}) {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${opts.token}`,
      Accept: 'application/json',
    }
    if (init.body) headers['Content-Type'] = 'application/json'
    if (init.idempotencyKey) headers['Idempotency-Key'] = init.idempotencyKey
    const res = await fetch(`${projectBase}${path}`, { ...init, headers: { ...headers, ...(init.headers as Record<string, string>) } })
    if (!res.ok) throw new OmnaraError(res.status, await res.text())
    return res.status === 204 ? null : ((await res.json()) as Json)
  }

  const enc = new TextEncoder()
  let key: Promise<CryptoKey> | null = null
  const hmacKey = () =>
    (key ??= crypto.subtle.importKey('raw', enc.encode(opts.sessionSecret || opts.token), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign', 'verify']))
  const b64url = (bytes: ArrayBuffer | Uint8Array) =>
    btoa(String.fromCharCode(...new Uint8Array(bytes))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  const fromB64url = (s: string) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0))

  async function signSession(user: GuideUser, agentId: string) {
    const payload = b64url(enc.encode(`${user.id}|${agentId}`))
    return `${payload}.${b64url(await crypto.subtle.sign('HMAC', await hmacKey(), enc.encode(payload)))}`
  }

  async function readAgent(req: Request, user: GuideUser): Promise<string | null> {
    const [payload, sig] = (req.headers.get('x-guide-session') ?? '').split('.')
    if (!payload || !sig) return null
    try {
      if (!(await crypto.subtle.verify('HMAC', await hmacKey(), fromB64url(sig), enc.encode(payload)))) return null
      // The agent ID is everything after the last "|": user IDs may contain one ("auth0|…").
      const decoded = new TextDecoder().decode(fromB64url(payload))
      const cut = decoded.lastIndexOf('|')
      const agentId = decoded.slice(cut + 1)
      return cut >= 0 && decoded.slice(0, cut) === user.id && ID.test(agentId) ? agentId : null
    } catch {
      return null
    }
  }

  /**
   * The key Omnara uses to recognise a retry. The browser's key is combined with the
   * signed-in user, so one user's key can never return another user's conversation.
   */
  async function requestKey(user: GuideUser, key: string) {
    const digest = await crypto.subtle.digest('SHA-256', enc.encode(JSON.stringify([user.id, key])))
    return `guide-${[...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('')}`
  }

  // Omnara rejects names with control, format or other invisible characters, such as the
  // variation selector inside a red heart emoji or a soft hyphen, so they are taken out
  // of anything used as a name.
  const oneLine = (text: string) =>
    text.replace(/\p{Cc}/gu, ' ').replace(/[\p{Cf}\p{Default_Ignorable_Code_Point}\u2800\ufffd]/gu, '').replace(/\s+/g, ' ').trim()
  /** Text cut to fit a name. Counted in whole characters, so an emoji is never cut in half. */
  function titleFrom(text: string, room: number, fallback = 'Conversation') {
    const chars = [...(oneLine(text) || fallback)]
    return chars.length > room ? `${chars.slice(0, room - 1).join('').trimEnd()}…` : chars.join('')
  }

  // ---- One agent profile per user -------------------------------------------------
  // A profile is named "<prefix> · <user's name> · <tag>". The tag is a short hash of the
  // user's ID and is what finds the profile again, so a user with no name, a renamed
  // user and two users with the same name each keep their own profile. The full user ID
  // is in a comment on the first line of the profile's config.
  type Profile = { id: string; name: string; current_config_id: string; current_config?: { source?: string } }
  const profiles = new Map<string, { id: string; source: string }>()
  const ownerLine = (user: GuideUser) => `# guide-user: ${JSON.stringify(user.id)}`
  const prefix = opts.profilePrefix ?? 'Guide'
  async function userTag(user: GuideUser) {
    const digest = await crypto.subtle.digest('SHA-256', enc.encode(user.id))
    return [...new Uint8Array(digest).slice(0, 4)].map((b) => b.toString(16).padStart(2, '0')).join('')
  }
  const profileName = async (user: GuideUser) =>
    `${prefix} · ${titleFrom(user.displayName ?? '', 64 - [...prefix].length - 14, 'User')} · ${await userTag(user)}`

  /** The guide's config with this user's details written into the instruction. */
  function configFor(user: GuideUser) {
    const about = [`Name: ${oneLine(user.displayName ?? 'Not given')}`, ...user.context.split('\n').map(oneLine).filter(Boolean)]
    const placeholder = opts.config.match(/^([ \t]*)\{\{user\}\}[ \t]*$/m)
    if (!placeholder) throw new Error('The guide config needs a line containing only {{user}} inside its instruction.')
    return `${ownerLine(user)}\n${opts.config.replace(placeholder[0], about.map((line) => `${placeholder[1]}${line}`).join('\n'))}`
  }

  /** The user's profile: the one whose name ends in their tag and whose config names them. */
  async function findProfile(user: GuideUser): Promise<Profile | null> {
    const glob = `${prefix.replace(/[\\*?]/g, '\\$&')} · * · ${await userTag(user)}`
    const { data } = (await omnara(`/agent-profiles?${new URLSearchParams({ name: glob, limit: '50' })}`)) as { data: Profile[] }
    return data.find((p) => p.current_config?.source?.startsWith(`${ownerLine(user)}\n`)) ?? null
  }

  /** The user's profile ID, creating the profile or bringing its config up to date first. */
  async function ensureProfile(user: GuideUser): Promise<string> {
    const source = configFor(user)
    const cached = profiles.get(user.id)
    if (cached?.source === source) return cached.id

    const newConfig = async () =>
      ((await omnara('/agent-configs', { method: 'POST', body: JSON.stringify({ source, source_format: 'yaml' }) })) as { id: string }).id
    let profile = await findProfile(user)
    if (!profile) {
      try {
        profile = (await omnara('/agent-profiles', { method: 'POST', body: JSON.stringify({ name: await profileName(user), config: await newConfig() }) })) as Profile
      } catch (err) {
        // Two first messages at once: the other one created it.
        profile = await findProfile(user)
        if (!profile) throw err
      }
    } else if (profile.current_config?.source !== source) {
      // The guide's config or the user's details changed since the profile was made.
      await omnara(`/agent-profiles/${profile.id}/config`, { method: 'POST', body: JSON.stringify({ config: await newConfig(), expected_current_config_id: profile.current_config_id }) })
    }
    profiles.set(user.id, { id: profile.id, source })
    return profile.id
  }

  const json = (body: unknown, init: ResponseInit = {}) =>
    new Response(JSON.stringify(body), { ...init, headers: { 'Content-Type': 'application/json', ...init.headers } })

  function actorFor(user: GuideUser) {
    return { provider_tenant_id: 'in-app-guide', provider_user_id: user.id, ...(user.displayName ? { display_name: user.displayName } : {}) }
  }

  async function handle(req: Request): Promise<Response> {
    const route = new URL(req.url).pathname.slice(basePath.length).split('/').filter(Boolean)
    const user = await opts.getUser(req)
    if (!user) return json({ error: 'not signed in' }, { status: 401 })
    const agentId = await readAgent(req, user)
    const agentPath = agentId ? `/agents/${agentId}` : null

    try {
      // POST /message: start the conversation or add to it.
      if (req.method === 'POST' && route[0] === 'message') {
        const body = (await req.json()) as { text: string; context: string; key: string; steering?: boolean }
        if (!body.text || !body.key) return json({ error: 'text and key are required' }, { status: 400 })
        const idempotencyKey = await requestKey(user, String(body.key))
        const content_blocks = [
          { type: 'text', text: body.text, metadata: { guide: 'question' } },
          { type: 'text', text: body.context, metadata: { guide: 'context' } },
        ]
        if (agentPath) {
          try {
            await omnara(`${agentPath}/inputs`, {
              method: 'POST',
              idempotencyKey,
              body: JSON.stringify({
                content_blocks,
                actor: actorFor(user),
                delivery_mode: body.steering ? 'steering' : 'queued',
                ...(body.steering ? { cancel_open_interactions: true } : {}),
              }),
            })
            return json({ ok: true, started: false })
          } catch (err) {
            // An archived or deleted agent: fall through and start a new conversation.
            if (!(err instanceof OmnaraError) || (err.status !== 404 && err.status !== 410 && err.status !== 409)) throw err
          }
        }
        const launched = (await omnara('/agents', {
          method: 'POST',
          idempotencyKey,
          body: JSON.stringify({
            profile: await ensureProfile(user),
            name: titleFrom(body.text, 64),
            initial_input: { content_blocks, actor: actorFor(user) },
          }),
        })) as { agent: { id: string } }
        return json({ ok: true, started: true, session: await signSession(user, launched.agent.id) })
      }

      // GET /conversations: this user's recent conversations, newest first.
      if (req.method === 'GET' && route[0] === 'conversations') {
        // A user's conversations are the agents launched from their own profile.
        const profileId = profiles.get(user.id)?.id ?? (await findProfile(user))?.id
        if (!profileId) return json({ data: [] })
        const query = new URLSearchParams({ agent_profile_id: profileId, sort: '-updated_at', limit: '30' })
        const res = (await omnara(`/agents?${query}`)) as { data: { id: string; name: string; updated_at: string; activity?: { last_activity_at?: string } }[] }
        const data = await Promise.all(
          res.data.map(async (a) => ({
            session: await signSession(user, a.id),
            title: a.name || 'Conversation',
            updatedAt: a.activity?.last_activity_at ?? a.updated_at,
            current: a.id === agentId,
          })),
        )
        data.sort((x, y) => y.updatedAt.localeCompare(x.updatedAt))
        return json({ data })
      }

      if (!agentPath) return json({ error: 'no conversation' }, { status: 404 })

      // GET /stream: relay the agent's event stream (SSE) to the browser.
      if (req.method === 'GET' && route[0] === 'stream') {
        const lastEventId = req.headers.get('last-event-id')
        const upstream = await fetch(`${projectBase}${agentPath}/events/stream?stream_deltas=true`, {
          headers: {
            Authorization: `Bearer ${opts.token}`,
            Accept: 'text/event-stream',
            ...(lastEventId && /^\d+$/.test(lastEventId) ? { 'Last-Event-ID': lastEventId } : {}),
          },
          signal: req.signal,
        })
        if (!upstream.ok || !upstream.body) throw new OmnaraError(upstream.status, await upstream.text())
        return new Response(upstream.body, {
          headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache, no-transform', 'X-Accel-Buffering': 'no' },
        })
      }

      // GET /history: the conversation so far, for rebuilding the panel after a reload.
      if (req.method === 'GET' && route[0] === 'history') {
        const messages: { role: 'user' | 'assistant'; text: string }[] = []
        let after = 0
        for (let page = 0; page < 10; page++) {
          const res = (await omnara(`${agentPath}/events?after_sequence=${after}&limit=500`)) as {
            data: Json[]
            next_after_sequence?: number
            has_more: boolean
          }
          for (const ev of res.data) {
            const blocks = (ev.content_blocks ?? []) as { type: string; text?: string; metadata?: Json }[]
            if (ev.event_kind === 'agent_input' && ev.input_kind === 'content') {
              const q = blocks.find((b) => b.type === 'text' && b.metadata?.guide === 'question') ?? blocks.find((b) => b.type === 'text')
              if (q?.text) messages.push({ role: 'user', text: q.text })
            } else if (ev.event_kind === 'model_output' && !blocks.some((b) => b.type === 'tool_call')) {
              const text = blocks.filter((b) => b.type === 'text' && b.text).map((b) => b.text).join('\n').trim()
              if (text) messages.push({ role: 'assistant', text })
            }
            after = Math.max(after, Number(ev.sequence) || 0)
          }
          if (!res.has_more) break
          after = res.next_after_sequence ?? after
        }
        return json({ messages, lastSequence: after })
      }

      // GET /tool-calls?state=ready|awaiting_permission: custom calls for the widget to run or preview.
      if (req.method === 'GET' && route[0] === 'tool-calls' && !route[1]) {
        const state = new URL(req.url).searchParams.get('state') === 'awaiting_permission' ? 'awaiting_permission' : 'ready'
        const res = (await omnara(`${agentPath}/tool-calls?state=${state}&type=custom&limit=20`)) as { data: Json[] }
        return json({ data: res.data.map(({ id, name, input, state }) => ({ id, name, input, state })) })
      }

      // POST /tool-calls/:id/result
      if (req.method === 'POST' && route[0] === 'tool-calls' && route[1] && route[2] === 'result') {
        if (!ID.test(route[1])) return json({ error: 'bad id' }, { status: 400 })
        const body = (await req.json()) as { outcome: 'succeeded' | 'failed'; text: string }
        try {
          await omnara(`${agentPath}/tool-calls/${route[1]}/result`, {
            method: 'POST',
            body: JSON.stringify({ outcome: body.outcome === 'failed' ? 'failed' : 'succeeded', content_blocks: [{ type: 'text', text: body.text }] }),
          })
        } catch (err) {
          // 409: the call already has a result or stopped being ready. Nothing to do.
          if (!(err instanceof OmnaraError) || err.status !== 409) throw err
        }
        return json({ ok: true })
      }

      // GET /interactions: open approvals for this conversation.
      if (req.method === 'GET' && route[0] === 'interactions' && !route[1]) {
        const res = (await omnara(`${agentPath}/interactions?state=open`)) as { data: Json[] }
        return json({
          data: res.data.map(({ id, tool_call_id, tool_name, interaction_kind, request }) => ({ id, tool_call_id, tool_name, interaction_kind, request })),
        })
      }

      // POST /interactions/:id/resolve
      if (req.method === 'POST' && route[0] === 'interactions' && route[1] && route[2] === 'resolve') {
        if (!ID.test(route[1])) return json({ error: 'bad id' }, { status: 400 })
        const body = (await req.json()) as { answers: { option_indices: number[]; text?: string }[] }
        await omnara(`${agentPath}/interactions/${route[1]}/resolve`, {
          method: 'POST',
          body: JSON.stringify({ answers: body.answers, actor: actorFor(user) }),
        })
        return json({ ok: true })
      }

      return json({ error: 'not found' }, { status: 404 })
    } catch (err) {
      const status = err instanceof OmnaraError ? err.status : 500
      console.error('[guide]', err)
      return json({ error: status === 402 ? 'The guide is out of credits.' : 'The guide is unavailable right now.' }, { status: 502 })
    }
  }

  return { GET: handle, POST: handle }
}
