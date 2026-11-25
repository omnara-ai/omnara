/** @vitest-environment happy-dom */

import type { AgentEvent, CustomToolCall } from '@omnara/sdk'
import { expect, it, vi } from 'vitest'

import {
  clickTool,
  createPageTools,
  type PageActionHost,
  type PageClick,
  pointAtTool,
  startedTurn,
} from './page-actions'
import type { PageMatch } from './page-snapshot'

function input(turnID: string, actorID: string | undefined): AgentEvent {
  // SAFETY: turn ownership reads only these fields of an input event.
  return { turn_id: turnID, event_kind: 'agent_input', actor_id: actorID } as AgentEvent
}
function output(turnID: string): AgentEvent {
  // SAFETY: turn ownership reads only these fields of a model output event.
  return { turn_id: turnID, event_kind: 'model_output' } as AgentEvent
}

it.each([
  { name: 'their own turn', events: [output('old'), input('t', 'me'), output('t')], older: true },
  { name: 'the first turn there is', events: [input('t', 'me'), output('t')], older: false },
])('says a person started $name', ({ events, older }) => {
  expect(startedTurn(events, 't', 'me', older)).toBe(true)
})

it.each([
  {
    name: 'someone else also spoke in the turn, even with nothing displayed',
    events: [output('old'), input('t', 'me'), input('t', 'them'), output('t')],
    older: true,
  },
  {
    name: 'something unattributed spoke in the turn',
    events: [output('old'), input('t', 'me'), input('t', undefined)],
    older: true,
  },
  { name: 'the start of the turn is not loaded', events: [output('t')], older: true },
  { name: 'the turn is someone else’s', events: [output('old'), input('t', 'them')], older: true },
  { name: 'the turn is not in the conversation', events: [input('other', 'me')], older: false },
])('does not say so when $name', ({ events, older }) => {
  expect(startedTurn(events, 't', 'me', older)).toBe(false)
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
  const windows = { thisOneHasTheCall: true }
  const host = {
    visible: vi.fn(() => true),
    startedTurn: vi.fn((_turnID: string) => true),
    timeoutMs: vi.fn((_tool: string): number | undefined => 5000),
    claim: <T>(_id: string, act: () => Promise<T>): Promise<T | undefined> =>
      windows.thisOneHasTheCall ? act() : Promise.resolve(undefined),
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
  ) {
    const cancel = new AbortController()
    const result = tools[name]({
      agentID: 'agt_1',
      toolCallID: id,
      turnID: 'trn_1',
      name,
      input: toolInput,
      signal: cancel.signal,
    })
    return { cancel, result }
  }
  return { host, element, windows, call }
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

it('keeps part of the tool’s timeout for the answer to arrive', async () => {
  const { host, call } = setup()
  const before = Date.now()
  await call(clickTool, { target: 'Models' }).result
  const deadline = host.find.mock.calls[0]?.[2].deadline
  expect(deadline).toBeGreaterThanOrEqual(before + 4000)
  expect(deadline).toBeLessThan(Date.now() + 5000)
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
    name: 'someone else started the turn',
    arrange: ({ host }) => host.startedTurn.mockReturnValue(false),
  },
  {
    name: 'another window has the call',
    arrange: ({ windows }) => {
      windows.thisOneHasTheCall = false
    },
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
