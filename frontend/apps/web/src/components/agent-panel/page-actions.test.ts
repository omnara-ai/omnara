/** @vitest-environment happy-dom */

import type { AgentEvent, CustomToolCall } from '@omnara/sdk'
import { expect, it, vi } from 'vitest'

import {
  askedFromHere,
  clickTool,
  createPageTools,
  type PageActionHost,
  type PageClick,
  pointAtTool,
} from './page-actions'
import type { PageMatch } from './page-snapshot'

const me = { actorID: 'me', sent: (key: string | undefined) => key === 'from-this-window' }
const call = { turnID: 't', toolCallID: 'tcl_1' }
let sequence = 0

function said(turnID: string, actorID: string | undefined, key?: string): AgentEvent {
  sequence += 1
  // SAFETY: ownership reads only these fields of an input event.
  return {
    turn_id: turnID,
    event_kind: 'agent_input',
    input_kind: 'content',
    actor_id: actorID,
    input_idempotency_key: key,
    sequence,
  } as AgentEvent
}
function called(turnID: string, ...toolCallIDs: string[]): AgentEvent {
  sequence += 1
  // SAFETY: ownership reads only these fields of a model output event.
  return {
    turn_id: turnID,
    event_kind: 'model_output',
    sequence,
    content_blocks: toolCallIDs.map((id) => ({ type: 'tool_call', tool_call_id: id })),
  } as AgentEvent
}

it.each([
  {
    name: 'their own turn',
    events: () => [called('old'), said('t', 'me', 'from-this-window'), called('t', 'tcl_1')],
    older: true,
  },
  {
    name: 'the first turn there is',
    events: () => [said('t', 'me', 'from-this-window'), called('t', 'tcl_1')],
    older: false,
  },
  {
    name: 'a turn they first spoke in from another window, then from this one',
    events: () => [
      called('old'),
      said('t', 'me', 'from-another-window'),
      said('t', 'me', 'from-this-window'),
      called('t', 'tcl_1'),
    ],
    older: true,
  },
])('carries out a call made in $name', ({ events, older }) => {
  expect(askedFromHere(events(), call, me, older)).toBe(true)
})

it.each([
  {
    name: 'someone else also spoke in the turn, even with nothing displayed',
    events: () => [
      called('old'),
      said('t', 'me', 'from-this-window'),
      said('t', 'them'),
      called('t', 'tcl_1'),
    ],
    older: true,
  },
  {
    name: 'something unattributed spoke in the turn',
    events: () => [
      called('old'),
      said('t', 'me', 'from-this-window'),
      said('t', undefined),
      called('t', 'tcl_1'),
    ],
    older: true,
  },
  {
    name: 'the same person sent the message from another window, whose outline the refs belong to',
    events: () => [called('old'), said('t', 'me', 'from-another-window'), called('t', 'tcl_1')],
    older: true,
  },
  {
    name: 'this window only spoke after the call was made',
    events: () => [
      called('old'),
      said('t', 'me', 'from-another-window'),
      called('t', 'tcl_1'),
      said('t', 'me', 'from-this-window'),
    ],
    older: true,
  },
  {
    name: 'the start of the turn is not loaded',
    events: () => [said('t', 'me', 'from-this-window'), called('t', 'tcl_1')],
    older: true,
  },
  {
    name: 'the call is not in the conversation',
    events: () => [called('old'), said('t', 'me', 'from-this-window'), called('t', 'tcl_other')],
    older: true,
  },
  {
    name: 'the turn is not in the conversation',
    events: () => [said('other', 'me', 'from-this-window'), called('other', 'tcl_1')],
    older: false,
  },
])('leaves a call alone when $name', ({ events, older }) => {
  expect(askedFromHere(events(), call, me, older)).toBe(false)
})

it('acts for nobody before it knows who is at the dashboard', () => {
  const events = [said('t', undefined, 'from-this-window'), called('t', 'tcl_1')]
  expect(askedFromHere(events, call, { ...me, actorID: undefined }, false)).toBe(false)
})

function deferred<T>() {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

function setup() {
  const element = document.createElement('button')
  const host = {
    visible: vi.fn(() => true),
    ours: vi.fn<PageActionHost['ours']>(() => true),
    read: vi.fn<PageActionHost['read']>(() => Promise.resolve({ ready: true, leftMs: 4000 })),
    find: vi.fn<PageActionHost['find']>(() => Promise.resolve({ kind: 'one', element })),
    click: vi.fn<PageActionHost['click']>(() =>
      Promise.resolve({ outcome: 'clicked', what: 'link "Models" [e1]', opened: '/models' }),
    ),
    point: vi.fn<PageActionHost['point']>(() => Promise.resolve()),
    describe: vi.fn((_element: Element) => 'button "New provider" primary [e2]'),
    snapshot: vi.fn(() => 'URL: /models'),
  } satisfies PageActionHost
  const tools = createPageTools(host)
  function call(
    name: typeof clickTool | typeof pointAtTool,
    toolInput: CustomToolCall['input'],
    id = 'tcl_1',
    answerBy: number | undefined = Date.now() + 5000,
  ) {
    const cancel = new AbortController()
    const result = tools[name]({
      agentID: 'agt_1',
      toolCallID: id,
      turnID: 'trn_1',
      name,
      input: toolInput,
      answerBy,
      signal: cancel.signal,
    })
    return { cancel, result }
  }
  return { host, element, call }
}

const text = (outcome: string, value: string) => ({
  outcome,
  content_blocks: [{ type: 'text', text: value }],
})

it('clicks what was asked for and answers with what the screen shows afterwards', async () => {
  const { host, element, call } = setup()
  await expect(call(clickTool, { target: 'Models' }).result).resolves.toEqual(
    text(
      'succeeded',
      'Clicked link "Models" [e1]. It opened /models.\n\nWhat the user sees now:\nURL: /models',
    ),
  )
  expect(host.find).toHaveBeenCalledExactlyOnceWith('Models', 'click', expect.anything())
  expect(host.click).toHaveBeenCalledExactlyOnceWith(element, expect.anything())
})

it('works to the time the call itself has left, keeping part of it for the answer to arrive', async () => {
  const { host, call } = setup()
  const answerBy = Date.now() + 1800
  await call(clickTool, { target: 'Models' }, 'tcl_1', answerBy).result
  expect(host.find.mock.calls[0]?.[2].deadline).toBe(answerBy - 500)
})

it.each([
  { now: { ready: true, leftMs: 4000 }, waiting: true },
  { now: { ready: true, leftMs: undefined }, waiting: true },
  { now: { ready: true, leftMs: 300 }, waiting: false },
  { now: { ready: false, leftMs: undefined }, waiting: false },
])('asks Omnara whether the call still waits: $now gives $waiting', async ({ now, waiting }) => {
  const { host, call } = setup()
  host.read.mockResolvedValue(now)
  await call(clickTool, { target: 'Models' }).result
  await expect(host.click.mock.calls[0]?.[1].stillWaiting()).resolves.toBe(waiting)
})

it('treats a call it cannot ask about as no longer waiting', async () => {
  const { host, call } = setup()
  host.read.mockRejectedValue(new Error('offline'))
  await call(clickTool, { target: 'Models' }).result
  await expect(host.click.mock.calls[0]?.[1].stillWaiting()).resolves.toBe(false)
})

const unresolved: { match: PageMatch; says: string }[] = [
  {
    match: { kind: 'several', refs: ['e4', 'e7'] },
    says: '2 different things on the user\'s screen match "Remove": e4, e7.',
  },
  { match: { kind: 'gone' }, says: '"Remove" is no longer on the user\'s screen.' },
  { match: { kind: 'none' }, says: 'Nothing matching "Remove" is on the user\'s screen.' },
]

it.each(unresolved)('clicks nothing and says why when the target is $match.kind', async (found) => {
  const { match, says } = found
  const { host, call } = setup()
  host.find.mockResolvedValue(match)
  const result = await call(clickTool, { target: 'Remove' }).result
  expect(result).toMatchObject({ outcome: 'failed' })
  expect(JSON.stringify(result)).toContain(JSON.stringify(says).slice(1, -1))
  expect(JSON.stringify(result)).toContain('What the user sees now:')
  expect(host.click).toHaveBeenCalledTimes(0)
})

it('passes on why a click was refused', async () => {
  const why = 'button "Delete" [e3] is disabled, so nothing was clicked.'
  const { host, call } = setup()
  host.click.mockResolvedValue({ outcome: 'refused', why })
  await expect(call(clickTool, { target: 'Delete' }).result).resolves.toEqual(
    text('failed', `${why}\n\nWhat the user sees now:\nURL: /models`),
  )
})

const notOurs: { name: string; arrange: (page: ReturnType<typeof setup>) => void }[] = [
  {
    name: 'the tab is not being looked at',
    arrange: ({ host }) => host.visible.mockReturnValue(false),
  },
  {
    name: 'the call is another person’s or another window’s',
    arrange: ({ host }) => host.ours.mockReturnValue(false),
  },
]

it.each(notOurs)('leaves a call alone when $name', async ({ arrange }) => {
  const page = setup()
  arrange(page)
  const { host, call } = page
  await expect(call(clickTool, { target: 'Models' }).result).resolves.toEqual({
    outcome: 'deferred',
  })
  expect(host.find).toHaveBeenCalledTimes(0)
})

it('answers a point while the cursor is still on its way, and holds the next call until it arrives', async () => {
  const arriving = deferred<undefined>()
  const { host, element, call } = setup()
  host.point.mockReturnValue(arriving.promise)
  await expect(
    call(pointAtTool, { target: 'New provider', note: 'Start here' }).result,
  ).resolves.toEqual(
    text('succeeded', 'Pointed at button "New provider" primary [e2]. The user can see it marked.'),
  )
  expect(host.point).toHaveBeenCalledExactlyOnceWith({ element, note: 'Start here' })
  const next = call(clickTool, { target: 'Models' }, 'tcl_2')
  await Promise.resolve()
  expect(host.find).toHaveBeenCalledTimes(1)
  arriving.resolve(undefined)
  await next.result
  expect(host.find).toHaveBeenCalledTimes(2)
})

it('never carries out a call that was called off while it waited for the one before it', async () => {
  const clicking = deferred<PageClick>()
  const { host, call } = setup()
  host.click.mockReturnValue(clicking.promise)
  const first = call(clickTool, { target: 'Models' })
  const second = call(clickTool, { target: 'Delete' }, 'tcl_2')
  const called = expect(second.result).rejects.toThrow('called off')
  second.cancel.abort(new Error('called off'))
  clicking.resolve({ outcome: 'clicked', what: 'link "Models" [e1]', opened: null })
  await expect(first.result).resolves.toMatchObject({ outcome: 'succeeded' })
  await called
  expect(host.find).toHaveBeenCalledExactlyOnceWith('Models', 'click', expect.anything())
  expect(host.click).toHaveBeenCalledTimes(1)
})
