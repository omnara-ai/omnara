// @ts-check
// A Primitive Function that wakes Omnara agents when email addresses them.
// One self-contained file with no dependencies: deploy it as is with
// `primitive functions deploy --file handler.js`.

/**
 * @typedef {import('@primitivedotdev/sdk').EmailReceivedEvent} EmailReceivedEvent
 * @typedef {EmailReceivedEvent['email']} Email
 * @typedef {'to' | 'cc' | 'cc, mentioned' | 'bcc'} Inclusion
 * @typedef {'addressed' | 'always' | 'never'} CcMode
 * @typedef {{
 *   PRIMITIVE_WEBHOOK_SECRET: string
 *   OMNARA_API_KEY: string
 *   OMNARA_ORG_ID: string
 *   OMNARA_PROJECT_ID: string
 *   AGENTS: string
 *   OMNARA_API_URL?: string
 *   CC_MODE?: string
 *   ALLOWED_SENDERS?: string
 *   SKIP_AUTOMATED?: string
 *   MAX_SPAM_SCORE?: string
 * }} Env
 * @typedef {{
 *   agents: Map<string, string>
 *   ccMode: CcMode
 *   allowedSenders: string[]
 *   skipAutomated: boolean
 *   maxSpamScore: number
 * }} Config
 * @typedef {<T>(method: string, path: string, body?: unknown, key?: string) => Promise<T>} Omnara
 */

export default {
  /** @param {Request} request @param {Env} env */
  async fetch(request, env) {
    try {
      return await handleEmail(request, env)
    } catch (error) {
      console.error(error instanceof Error ? error.message : error)
      return new Response('email handler failed', { status: 500 })
    }
  },
}

/** @param {Request} request @param {Env} env @param {typeof fetch} [send] */
export async function handleEmail(request, env, send = fetch) {
  const rawBody = await request.text()
  const signature = request.headers.get('primitive-signature') ?? ''
  if (!(await validSignature(rawBody, signature, env.PRIMITIVE_WEBHOOK_SECRET))) {
    console.log('rejected: invalid signature')
    return new Response('invalid signature', { status: 401 })
  }

  /** @type {EmailReceivedEvent} */
  const event = JSON.parse(rawBody)
  if (event.event !== 'email.received') return skipped(`event ${event.id}`, 'not an email.received event')

  const config = readConfig(env)
  const email = event.email
  const label = `email ${email.id}`
  const sender = bareAddress(email.headers.from)
  const headers = rawHeaders(email)

  const spamScore = email.analysis.spamassassin?.score ?? 0
  if (spamScore >= config.maxSpamScore) return skipped(label, `spam score ${spamScore}`)
  if (config.skipAutomated && isAutomated(email, sender, headers)) return skipped(label, 'automated mail')
  if (config.allowedSenders.length > 0) {
    const allowed = senderAllowed(email, sender, config.allowedSenders)
    if (allowed === 'retry') {
      console.log(`${label}: DMARC check failed temporarily, asking Primitive to retry`)
      return new Response('sender check failed, retry', { status: 503 })
    }
    if (allowed === 'no') return skipped(label, 'sender not in ALLOWED_SENDERS')
  }

  const to = addresses(email.parsed.to_addresses, headers.get('to') ?? email.headers.to)
  const cc = addresses(email.parsed.cc, headers.get('cc'))
  const omnara = omnaraClient(env, send)
  /** @type {string[]} */
  const woke = []
  for (const recipient of new Set(email.smtp.rcpt_to.map(bareAddress))) {
    const profileID = agentFor(config.agents, recipient)
    if (!profileID || recipient === sender) continue
    const inclusion = includedAs(recipient, to, cc, config.ccMode, email)
    if (!inclusion) continue
    await wake(omnara, email, recipient, profileID, inclusion, sender)
    woke.push(recipient)
  }
  if (woke.length === 0) return skipped(label, 'no agent was addressed')
  console.log(`${label}: woke ${woke.join(', ')}`)
  return Response.json({ woke })
}

// Primitive-Signature is `t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>.<body>">`.
/** @param {string} rawBody @param {string} header @param {string} secret */
async function validSignature(rawBody, header, secret) {
  if (!secret) throw new Error('PRIMITIVE_WEBHOOK_SECRET is not set')
  let timestamp = NaN
  /** @type {string[]} */
  const signatures = []
  for (const part of header.split(',')) {
    const [key, value = ''] = part.split('=', 2).map((piece) => piece.trim())
    if (key === 't' && /^\d{1,12}$/.test(value)) timestamp = Number(value)
    if (key === 'v1' && /^[0-9a-f]{64}$/i.test(value)) signatures.push(value)
  }
  const age = Math.floor(Date.now() / 1000) - timestamp
  if (!(age <= 300 && age >= -60)) return false

  const encoder = new TextEncoder()
  const key = await crypto.subtle.importKey(
    'raw',
    encoder.encode(secret),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['verify'],
  )
  const payload = encoder.encode(`${timestamp}.${rawBody}`)
  for (const signature of signatures) {
    const bytes = new Uint8Array(signature.match(/../g)?.map((byte) => parseInt(byte, 16)) ?? [])
    if (await crypto.subtle.verify('HMAC', key, bytes, payload)) return true
  }
  return false
}

/** @param {Env} env @returns {Config} */
function readConfig(env) {
  /** @type {Map<string, string>} */
  const agents = new Map()
  for (const [address, profileID] of Object.entries(JSON.parse(env.AGENTS || '{}'))) {
    if (typeof profileID !== 'string' || !profileID) {
      throw new Error(`AGENTS["${address}"] must be an agent profile ID`)
    }
    agents.set(address.trim().toLowerCase(), profileID)
  }
  if (agents.size === 0) throw new Error('AGENTS must map at least one address to a profile')

  const ccMode = (env.CC_MODE || 'addressed').toLowerCase()
  if (ccMode !== 'addressed' && ccMode !== 'always' && ccMode !== 'never') {
    throw new Error('CC_MODE must be addressed, always, or never')
  }
  const maxSpamScore = Number(env.MAX_SPAM_SCORE || 5)
  if (!Number.isFinite(maxSpamScore)) throw new Error('MAX_SPAM_SCORE must be a number')

  return {
    agents,
    ccMode,
    allowedSenders: (env.ALLOWED_SENDERS ?? '')
      .split(',')
      .map((entry) => entry.trim().toLowerCase())
      .filter(Boolean),
    skipAutomated: (env.SKIP_AUTOMATED ?? 'true').toLowerCase() !== 'false',
    maxSpamScore,
  }
}

/** @param {Map<string, string>} agents @param {string} address */
function agentFor(agents, address) {
  return agents.get(address) ?? agents.get(address.split('@')[0]) ?? agents.get('*')
}

/**
 * @param {string} address @param {Set<string>} to @param {Set<string>} cc
 * @param {CcMode} ccMode @param {Email} email
 * @returns {Inclusion | undefined}
 */
function includedAs(address, to, cc, ccMode, email) {
  if (to.has(address)) return 'to'
  if (!cc.has(address)) return 'bcc'
  if (ccMode === 'always') return 'cc'
  if (ccMode === 'never') return undefined
  return mentions(newText(email), address) ? 'cc, mentioned' : undefined
}

/** @param {string} text @param {string} address */
function mentions(text, address) {
  const local = address.split('@')[0]
  const handle = new RegExp(`(^|[^\\w.@+-])@${escapeRegExp(local)}(?![\\w@+-]|\\.\\w)`, 'i')
  return text.toLowerCase().includes(address) || handle.test(text)
}

// The part of the email its sender just wrote: quoted replies and forwarded
// messages are dropped so an old @mention doesn't wake the agent again.
/** @param {Email} email */
function newText(email) {
  const text = email.parsed.body_text ?? htmlText(email.parsed.body_html ?? '')
  /** @type {string[]} */
  const kept = []
  for (const line of text.split(/\r?\n/)) {
    if (/^\s*>/.test(line)) continue
    if (/^\s*-{2,}\s*(original|forwarded) message\s*-{2,}/i.test(line)) break
    kept.push(line)
  }
  const body = kept.join('\n')
  const quoteStart = [
    /^On\b[^\n]*(?:\n[^\n]*)?\bwrote:\s*$/im,
    /^From:\s[^\n]*\n(?:Sent|Date):\s/im,
  ]
    .map((pattern) => pattern.exec(body)?.index ?? -1)
    .filter((index) => index >= 0)
  return quoteStart.length ? body.slice(0, Math.min(...quoteStart)) : body
}

/** @param {string} html */
function htmlText(html) {
  const quote = html.search(/<blockquote|class="gmail_quote|id="divRplyFwdMsg/i)
  return (quote >= 0 ? html.slice(0, quote) : html)
    .replace(/<(br|\/p|\/div)[^>]*>/gi, '\n')
    .replace(/<[^>]*>/g, ' ')
    .replace(/&(#64|commat);/gi, '@')
    .replace(/&nbsp;/gi, ' ')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&amp;/gi, '&')
}

/** @param {Email} email @param {string} sender @param {Map<string, string>} headers */
function isAutomated(email, sender, headers) {
  if (/^(no-?reply|do-?not-?reply|mailer-daemon|postmaster|bounces?)([+.-]|@)/.test(sender)) {
    return true
  }
  if (email.smtp.mail_from === '' || email.smtp.mail_from === '<>') return true
  const autoSubmitted = headers.get('auto-submitted')?.toLowerCase()
  if (autoSubmitted && autoSubmitted !== 'no') return true
  if (/^(bulk|list|junk|auto_reply)$/i.test(headers.get('precedence') ?? '')) return true
  return headers.has('list-id') || headers.has('x-autoreply') || headers.has('x-autorespond')
}

// An allowlisted sender must also pass DMARC as its own domain, so a forged
// From header (or an allowlisted address hidden in a display name) is refused.
/** @param {Email} email @param {string} sender @param {string[]} entries */
function senderAllowed(email, sender, entries) {
  if (email.auth.dmarc === 'temperror') return 'retry'
  const domain = sender.slice(sender.lastIndexOf('@') + 1)
  const fromAddresses = email.headers.from
    .replace(/"[^"]*"/g, '')
    .match(/[^\s<>,;:"']+@[^\s<>,;:"']+/g)
  if (
    email.auth.dmarc !== 'pass' ||
    email.auth.dmarcFromDomain?.toLowerCase() !== domain ||
    fromAddresses?.length !== 1
  ) {
    return 'no'
  }
  const listed = entries.some((entry) =>
    entry.startsWith('@') || !entry.includes('@')
      ? entry.replace(/^@/, '') === domain
      : entry === sender,
  )
  return listed ? 'yes' : 'no'
}

// Header names from the raw message, for the headers the parsed payload
// doesn't carry (Cc fallback, Auto-Submitted, List-Id, Precedence).
/** @param {Email} email */
function rawHeaders(email) {
  /** @type {Map<string, string>} */
  const headers = new Map()
  const raw = email.content.raw
  if (!raw?.included) return headers
  let decoded
  try {
    decoded = atob(raw.data.slice(0, 87_380))
  } catch {
    return headers
  }
  const end = decoded.search(/\r?\n\r?\n/)
  const block = (end >= 0 ? decoded.slice(0, end) : decoded).replace(/\r?\n[ \t]+/g, ' ')
  for (const line of block.split(/\r?\n/)) {
    const colon = line.indexOf(':')
    if (colon <= 0) continue
    const name = line.slice(0, colon).trim().toLowerCase()
    if (!headers.has(name)) headers.set(name, line.slice(colon + 1).trim())
  }
  return headers
}

/** @param {{ address: string }[] | null} parsed @param {string | null | undefined} header */
function addresses(parsed, header) {
  if (parsed) return new Set(parsed.map((entry) => bareAddress(entry.address)))
  return new Set((header ?? '').match(/[^\s<>,;:"']+@[^\s<>,;:"']+/g)?.map(bareAddress) ?? [])
}

/** @param {string} value */
function bareAddress(value) {
  const address = /<([^>]+)>/.exec(value)?.[1] ?? value
  return address.replace(/[^\x21-\x7e]/g, '').toLowerCase().slice(0, 254)
}

/** @param {string} from */
function displayName(from) {
  const name = /^\s*"?([^"<]*?)"?\s*</.exec(from)?.[1] ?? ''
  return cleanName(name, 256)
}

/** @param {Env} env @param {typeof fetch} send @returns {Omnara} */
function omnaraClient(env, send) {
  const base = `${env.OMNARA_API_URL || 'https://api.omnara.com/v1'}/orgs/${env.OMNARA_ORG_ID}/projects/${env.OMNARA_PROJECT_ID}`
  return async (method, path, body, key) => {
    /** @type {Record<string, string>} */
    const headers = { authorization: `Bearer ${env.OMNARA_API_KEY}` }
    if (body !== undefined) headers['content-type'] = 'application/json'
    if (key) headers['idempotency-key'] = key
    const response = await send(`${base}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    if (!response.ok) {
      throw new Error(`Omnara ${method} ${path} returned ${response.status}: ${await response.text()}`)
    }
    return response.json()
  }
}

/**
 * @param {Omnara} omnara @param {Email} email @param {string} address
 * @param {string} profileID @param {Inclusion} inclusion @param {string} sender
 */
async function wake(omnara, email, address, profileID, inclusion, sender) {
  const thread = email.thread_id ?? email.id
  /** @type {{ current_config_id: string }} */
  const profile = await omnara('GET', `/agent-profiles/${profileID}`)
  const subject = (email.headers.subject ?? '').replace(/^\s*((re|fwd?|aw|sv)\s*:\s*)+/i, '')
  const name = cleanName(`${address.split('@')[0]}: ${subject || `email from ${sender}`}`, 64)
  /** @param {string} key @returns {Promise<{ id: string, state: string }>} */
  const launch = async (key) => {
    /** @type {{ agent: { id: string, state: string } }} */
    const response = await omnara(
      'POST',
      '/agents',
      { profile: profileID, config: profile.current_config_id, ...(name && { name }) },
      key,
    )
    return response.agent
  }
  let agent = await launch(await idempotencyKey('thread', address, thread))
  if (agent.state === 'archived') {
    agent = await launch(await idempotencyKey('thread', address, thread, email.id))
  }

  const actorName = displayName(email.headers.from)
  await omnara(
    'POST',
    `/agents/${agent.id}/inputs`,
    {
      content_blocks: [
        {
          type: 'text',
          text: [
            `New email to ${address}`,
            `email_id: ${email.id}`,
            `thread_id: ${email.thread_id ?? 'none'}`,
            `from: ${sender}`,
            `included: ${inclusion}`,
          ].join('\n'),
        },
      ],
      actor: {
        provider_tenant_id: (sender.split('@')[1] || 'email').slice(0, 128),
        provider_user_id: sender.slice(0, 128) || 'unknown',
        ...(actorName && { display_name: actorName }),
      },
    },
    await idempotencyKey('input', address, email.id),
  )
}

/** @param {string} value @param {number} maxLength */
function cleanName(value, maxLength) {
  const visible = value
    .normalize('NFC')
    .replace(/\s+/g, ' ')
    .replace(/[\p{Cc}\p{Cf}\p{Cs}\p{Default_Ignorable_Code_Point}\u2800\ufffd]/gu, '')
  return Array.from(visible.trim()).slice(0, maxLength).join('').trim()
}

/** @param {string} kind @param {...string} parts */
async function idempotencyKey(kind, ...parts) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(parts.join('\n')))
  const hex = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0'))
  return `primitive-email-${kind}:${hex.join('')}`
}

/** @param {string} value */
function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** @param {string} label @param {string} reason */
function skipped(label, reason) {
  console.log(`${label}: skipped, ${reason}`)
  return Response.json({ skipped: reason })
}
