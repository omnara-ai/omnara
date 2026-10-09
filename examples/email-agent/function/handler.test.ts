import assert from 'node:assert/strict'
import { mock, test } from 'node:test'

import { signWebhookPayload, type EmailReceivedEvent } from '@primitivedotdev/sdk'

import { handleEmail, type Env } from './handler.js'

const secret = 'test-webhook-secret'
const agent = 'assistant@acme.primitive.email'

const env: Env = {
  PRIMITIVE_WEBHOOK_SECRET: secret,
  OMNARA_API_KEY: 'omnara_org_v1_test',
  OMNARA_ORG_ID: 'org_test',
  OMNARA_PROJECT_ID: 'proj_test',
  OMNARA_API_URL: 'https://omnara.test/v1',
  AGENTS: JSON.stringify({ assistant: 'aprf_assistant' }),
}

const logs = mock.method(console, 'log', () => {})

type Call = { method: string; path: string; key?: string; body?: any }

function omnara(options: { archived?: Set<string>; fail?: boolean } = {}) {
  const calls: Call[] = []
  const agents = new Map<string, string>()
  const send = (async (url: string, init: RequestInit) => {
    const headers = init.headers as Record<string, string>
    const call: Call = {
      method: init.method ?? 'GET',
      path: url.replace('https://omnara.test/v1/orgs/org_test/projects/proj_test', ''),
      key: headers['idempotency-key'],
      body: init.body ? JSON.parse(init.body as string) : undefined,
    }
    calls.push(call)
    if (options.fail) return new Response('boom', { status: 503 })
    if (call.path.startsWith('/agent-profiles/')) {
      return Response.json({ id: call.path.split('/')[2], current_config_id: 'acfg_1' })
    }
    if (call.path === '/agents') {
      if (!agents.has(call.key!)) agents.set(call.key!, `agt_${agents.size + 1}`)
      const id = agents.get(call.key!)!
      return Response.json({ agent: { id, state: options.archived?.has(id) ? 'archived' : 'active' } })
    }
    return Response.json({ id: 'inp_1' })
  }) as typeof fetch
  return { calls, send }
}

type Message = {
  id?: string
  threadID?: string | null
  from?: string
  to?: string[]
  cc?: string[]
  rcpt?: string[]
  body?: string
  rawHeaders?: string[]
  spamScore?: number
  dmarc?: { pass: boolean; domain: string }
}

function received(message: Message = {}): EmailReceivedEvent {
  const from = message.from ?? 'Dana R <dana@example.com>'
  const to = message.to ?? [agent]
  const cc = message.cc ?? []
  const body = message.body ?? 'Can you pull the Q3 numbers?'
  const raw = [
    `From: ${from}`,
    `To: ${to.join(', ')}`,
    ...(cc.length ? [`Cc: ${cc.join(', ')}`] : []),
    'Subject: Re: Q3 numbers',
    ...(message.rawHeaders ?? []),
    '',
    body,
  ].join('\r\n')
  const data = Buffer.from(raw).toString('base64')
  return {
    id: `evt_${message.id ?? 'em_1'}`,
    event: 'email.received',
    version: '2025-01-01',
    delivery: { endpoint_id: 'ep_1', attempt: 1, attempted_at: '2026-10-07T00:00:00Z' },
    email: {
      id: message.id ?? 'em_1',
      thread_id: message.threadID === undefined ? 'thr_1' : message.threadID,
      received_at: '2026-10-07T00:00:00Z',
      smtp: {
        helo: 'mail.example.com',
        mail_from: 'bounce@example.com',
        rcpt_to: (message.rcpt ?? [agent]) as [string, ...string[]],
      },
      headers: {
        message_id: '<m1@example.com>',
        subject: 'Re: Q3 numbers',
        from,
        to: to.join(', '),
        date: null,
      },
      content: {
        raw: {
          included: true,
          encoding: 'base64',
          max_inline_bytes: 1_000_000,
          size_bytes: raw.length,
          sha256: '',
          data,
        },
        download: null,
      },
      parsed: {
        status: 'complete',
        error: null,
        body_text: body,
        body_html: null,
        reply_to: null,
        cc: cc.length ? cc.map((address) => ({ address, name: null })) : null,
        bcc: null,
        to_addresses: to.map((address) => ({ address, name: null })),
        in_reply_to: null,
        references: null,
        attachments: [],
        attachments_download_url: null,
      },
      analysis: { spamassassin: { score: message.spamScore ?? 0 } },
      auth: {
        spf: message.dmarc?.pass ? 'pass' : 'none',
        dmarc: message.dmarc?.pass ? 'pass' : 'none',
        dmarcPolicy: message.dmarc ? 'reject' : null,
        dmarcFromDomain: message.dmarc?.domain ?? null,
        dmarcSpfAligned: !!message.dmarc?.pass,
        dmarcDkimAligned: !!message.dmarc?.pass,
        dmarcSpfStrict: null,
        dmarcDkimStrict: null,
        dkimSignatures: [],
      },
    },
  } as EmailReceivedEvent
}

async function deliver(
  event: EmailReceivedEvent,
  send: typeof fetch,
  overrides: Partial<Env> = {},
  signature?: string,
) {
  const rawBody = JSON.stringify(event)
  const request = new Request('https://function.test/', {
    method: 'POST',
    headers: { 'Primitive-Signature': signature ?? signWebhookPayload(rawBody, secret).header },
    body: rawBody,
  })
  const response = await handleEmail(request, { ...env, ...overrides }, send)
  return { status: response.status, body: await response.json().catch(() => null) }
}

const inputs = (calls: Call[]) => calls.filter((call) => call.path.endsWith('/inputs'))
const launches = (calls: Call[]) => calls.filter((call) => call.path === '/agents')

test('rejects requests without a valid signature', async () => {
  const event = received()
  const rawBody = JSON.stringify(event)
  const signatures = [
    '',
    't=1,v1=bad',
    signWebhookPayload(rawBody, secret, Math.floor(Date.now() / 1000) - 600).header,
    signWebhookPayload(rawBody, 'some-other-secret').header,
    signWebhookPayload(`${rawBody} `, secret).header,
  ]
  for (const signature of signatures) {
    const { calls, send } = omnara()
    const { status } = await deliver(event, send, {}, signature)
    assert.equal(status, 401, signature)
    assert.equal(calls.length, 0)
  }
})

test('ignores events other than email.received', async () => {
  const { calls, send } = omnara()
  const { body } = await deliver({ ...received(), event: 'email.bounced' }, send)
  assert.deepEqual(body, { skipped: 'not an email.received event' })
  assert.equal(calls.length, 0)
})

test('mail to the agent starts a thread conversation and delivers the email', async () => {
  const { calls, send } = omnara()
  const { status, body } = await deliver(received(), send)
  assert.equal(status, 200)
  assert.deepEqual(body, { woke: [agent] })
  assert.deepEqual(launches(calls)[0].body, {
    profile: 'aprf_assistant',
    config: 'acfg_1',
    name: 'assistant: Q3 numbers',
  })
  const [input] = inputs(calls)
  assert.equal(input.path, '/agents/agt_1/inputs')
  assert.deepEqual(input.body, {
    content_blocks: [
      {
        type: 'text',
        text: `New email to ${agent}\nemail_id: em_1\nthread_id: thr_1\nfrom: dana@example.com\nincluded: to`,
      },
    ],
    actor: { provider_tenant_id: 'example.com', provider_user_id: 'dana@example.com', display_name: 'Dana R' },
  })
})

test('later emails in a thread reach the same agent, and redelivery is a no-op', async () => {
  const { calls, send } = omnara()
  await deliver(received(), send)
  await deliver(received(), send)
  await deliver(received({ id: 'em_2' }), send)
  const launchKeys = launches(calls).map((call) => call.key)
  assert.equal(new Set(launchKeys).size, 1)
  const inputKeys = inputs(calls).map((call) => call.key)
  assert.equal(inputKeys[0], inputKeys[1])
  assert.notEqual(inputKeys[0], inputKeys[2])
  assert.deepEqual(
    inputs(calls).map((call) => call.path),
    ['/agents/agt_1/inputs', '/agents/agt_1/inputs', '/agents/agt_1/inputs'],
  )
})

test('an email without a thread_id gets its own conversation', async () => {
  const { calls, send } = omnara()
  await deliver(received({ threadID: null }), send)
  await deliver(received({ id: 'em_2', threadID: null }), send)
  assert.deepEqual(
    inputs(calls).map((call) => call.path),
    ['/agents/agt_1/inputs', '/agents/agt_2/inputs'],
  )
})

test('an archived thread conversation is replaced by a new one', async () => {
  const { calls, send } = omnara({ archived: new Set(['agt_1']) })
  await deliver(received(), send)
  assert.equal(launches(calls).length, 2)
  assert.notEqual(launches(calls)[0].key, launches(calls)[1].key)
  assert.equal(inputs(calls)[0].path, '/agents/agt_2/inputs')
})

test('a Cc wakes the agent only when the new text mentions it', async () => {
  const cc = { to: ['bob@example.com'], cc: [agent], rcpt: [agent] }

  const quiet = omnara()
  assert.deepEqual((await deliver(received({ ...cc, body: 'Looping in our assistant.' }), quiet.send)).body, {
    skipped: 'no agent was addressed',
  })

  const quoted = omnara()
  await deliver(
    received({ ...cc, body: 'Sounds good.\n\nOn Mon, Oct 5, 2026 at 9:00 AM Dana wrote:\n> @assistant can you check?' }),
    quoted.send,
  )
  assert.equal(inputs(quoted.calls).length, 0)

  const mentioned = omnara()
  await deliver(received({ ...cc, body: '@assistant can you pull last quarter too?' }), mentioned.send)
  assert.match(inputs(mentioned.calls)[0].body.content_blocks[0].text, /included: cc, mentioned$/)

  const always = omnara()
  await deliver(received({ ...cc, body: 'Looping in our assistant.' }), always.send, { CC_MODE: 'always' })
  assert.match(inputs(always.calls)[0].body.content_blocks[0].text, /included: cc$/)

  const never = omnara()
  await deliver(received({ ...cc, body: '@assistant hi' }), never.send, { CC_MODE: 'never' })
  assert.equal(inputs(never.calls).length, 0)
})

test('a Bcc wakes the agent and says so', async () => {
  const { calls, send } = omnara()
  await deliver(received({ to: ['bob@example.com'], rcpt: [agent] }), send)
  assert.match(inputs(calls)[0].body.content_blocks[0].text, /included: bcc$/)
})

test('automated mail, spam, and the agent’s own mail are skipped', async () => {
  const cases: [Message, string][] = [
    [{ rawHeaders: ['Auto-Submitted: auto-replied'] }, 'automated mail'],
    [{ rawHeaders: ['List-Id: <news.example.com>'] }, 'automated mail'],
    [{ from: 'no-reply@example.com' }, 'automated mail'],
    [{ spamScore: 7.5 }, 'spam score 7.5'],
    [{ from: agent }, 'no agent was addressed'],
  ]
  for (const [message, reason] of cases) {
    const { calls, send } = omnara()
    assert.deepEqual((await deliver(received(message), send)).body, { skipped: reason })
    assert.equal(calls.length, 0)
  }

  const { calls, send } = omnara()
  await deliver(received({ rawHeaders: ['Auto-Submitted: auto-replied'] }), send, { SKIP_AUTOMATED: 'false' })
  assert.equal(inputs(calls).length, 1)
})

test('ALLOWED_SENDERS admits only authenticated senders on the list', async () => {
  const allowed = { ALLOWED_SENDERS: '@example.com, ops@partner.test' }

  const trusted = omnara()
  await deliver(received({ dmarc: { pass: true, domain: 'example.com' } }), trusted.send, allowed)
  assert.equal(inputs(trusted.calls).length, 1)

  const unauthenticated = omnara()
  assert.deepEqual((await deliver(received(), unauthenticated.send, allowed)).body, {
    skipped: 'sender not in ALLOWED_SENDERS',
  })

  for (const from of [
    'eve@evil.test',
    '"ops@partner.test" <eve@evil.test>',
    'ops@partner.test <eve@evil.test>',
  ]) {
    const outsider = omnara()
    await deliver(received({ from, dmarc: { pass: true, domain: 'evil.test' } }), outsider.send, allowed)
    assert.equal(outsider.calls.length, 0, from)
  }

  const exact = omnara()
  await deliver(
    received({ from: 'Ops <ops@partner.test>', dmarc: { pass: true, domain: 'partner.test' } }),
    exact.send,
    allowed,
  )
  assert.equal(inputs(exact.calls).length, 1)
})

test('each addressed agent gets its own conversation', async () => {
  const sales = 'sales@acme.primitive.email'
  const { calls, send } = omnara()
  const { body } = await deliver(
    received({ to: [agent, sales], rcpt: [agent, sales] }),
    send,
    { AGENTS: JSON.stringify({ assistant: 'aprf_assistant', '*': 'aprf_desk' }) },
  )
  assert.deepEqual(body, { woke: [agent, sales] })
  assert.deepEqual(
    launches(calls).map((call) => [call.body.profile, call.body.name]),
    [
      ['aprf_assistant', 'assistant: Q3 numbers'],
      ['aprf_desk', 'sales: Q3 numbers'],
    ],
  )
  assert.notEqual(launches(calls)[0].key, launches(calls)[1].key)
})

test('mail to an address with no agent is ignored', async () => {
  const { calls, send } = omnara()
  const other = 'billing@acme.primitive.email'
  const { body } = await deliver(received({ to: [other], rcpt: [other] }), send)
  assert.deepEqual(body, { skipped: 'no agent was addressed' })
  assert.equal(calls.length, 0)
})

test('each delivery logs which agents it woke or why it skipped', async () => {
  const { send } = omnara()
  logs.mock.resetCalls()
  await deliver(received(), send)
  await deliver(received({ id: 'em_2', spamScore: 9 }), send)
  await deliver(received({ id: 'em_3', to: ['bob@example.com'], cc: [agent] }), send)
  assert.deepEqual(
    logs.mock.calls.map((call) => call.arguments[0]),
    [
      `email em_1: woke ${agent}`,
      'email em_2: skipped, spam score 9',
      'email em_3: skipped, no agent was addressed',
    ],
  )
})

test('an Omnara API failure surfaces so Primitive retries the delivery', async () => {
  const { send } = omnara({ fail: true })
  await assert.rejects(deliver(received(), send), /returned 503/)
})
