/** @vitest-environment happy-dom */

import type { OmnaraUIMessage } from '@omnara/react'
import { expect, it } from 'vitest'

import {
  answerWhenReady,
  createPageActionRunner,
  type PageAction,
  pageActionsIn,
  type PageClick,
  type PagePointer,
} from './page-actions'

function said(id: string, actorId: string | undefined): OmnaraUIMessage {
  return {
    id,
    role: 'user',
    metadata: {
      eventId: id,
      eventKind: 'agent_input',
      inputKind: 'content',
      actorId,
      sequence: 1,
      turnId: id,
      turnSequence: 1,
      createdAt: '2026-11-23T00:00:00Z',
    },
    parts: [{ type: 'text', id: `${id}-text`, text: 'Where are models?' }],
  }
}

function call(
  id: string,
  toolName: string,
  input: Record<string, string>,
  options: { completed?: boolean; custom?: boolean } = {},
): OmnaraUIMessage {
  return {
    id: `message-${id}`,
    role: 'assistant',
    parts: [
      {
        type: 'dynamic-tool',
        id: `part-${id}`,
        toolCallId: id,
        toolName,
        toolType: options.custom === false ? 'mcp' : 'custom',
        input,
        ...(options.completed === true
          ? { state: 'output-available', output: { contentBlocks: [] } }
          : { state: 'input-available' }),
      },
    ],
  }
}

it('reads the page tool calls from the turns this person started, in order', () => {
  expect(
    pageActionsIn(
      [
        said('theirs', 'act_other'),
        call('not-mine', 'dashboard_click', { target: 'Usage' }),
        said('mine', 'act_me'),
        call('one', 'dashboard_click', { target: 'Models' }),
        call('two', 'dashboard_click', { target: 'Agents' }, { completed: true }),
        call('three', 'dashboard_point_at', { target: 'New model', note: 'Start here' }),
        call('empty', 'dashboard_point_at', { target: '' }),
        call('mcp', 'dashboard_click', { target: 'Usage' }, { custom: false }),
        call('other', 'create_ticket', { target: 'Models' }),
      ],
      'act_me',
    ),
  ).toEqual([
    { id: 'one', tool: 'dashboard_click', target: 'Models', waiting: true },
    { id: 'two', tool: 'dashboard_click', target: 'Agents', waiting: false },
    { id: 'three', tool: 'dashboard_point_at', target: 'New model', note: 'Start here' },
  ])
})

const clicks = new Map<string, PageClick>([
  ['Models', { outcome: 'clicked', what: 'link "Models"', opened: '/models' }],
  ['Omnara Owner', { outcome: 'clicked', what: 'button "Omnara Owner" collapsed', opened: null }],
  [
    'Save',
    { outcome: 'refused', why: 'button "Save" disabled is disabled, so nothing was clicked.' },
  ],
])

function page(found: Record<string, Element>) {
  const log: string[] = []
  const answers: string[] = []
  let pointer: PagePointer | null = null
  let visible = true
  const runner = createPageActionRunner({
    visible: () => visible,
    click: (target) => {
      log.push(`click ${target}`)
      return Promise.resolve(clicks.get(target) ?? { outcome: 'missing' })
    },
    find: (target) => {
      log.push(`find ${target}`)
      return Promise.resolve(found[target] ?? null)
    },
    point: (next) => {
      pointer = next
    },
    snapshot: () => '# Providers\nbutton "New provider"',
    answer: (id, outcome, text) => {
      answers.push(`${id} ${outcome}: ${text}`)
      return Promise.resolve()
    },
  })
  return {
    runner,
    log,
    answers,
    pointer: () => pointer,
    show: (shown: boolean) => {
      visible = shown
    },
  }
}

const outline = 'What the user sees now:\n# Providers\nbutton "New provider"'
const click = (id: string, target: string, waiting = true): PageAction => ({
  id,
  tool: 'dashboard_click',
  target,
  waiting,
})
const history: PageAction[] = [click('old', 'Usage')]
const point: PageAction = {
  id: 'point',
  tool: 'dashboard_point_at',
  target: 'New provider',
  note: 'Start here',
}

it('clicks and answers with what the screen shows afterwards, and points without answering', async () => {
  const button = document.createElement('button')
  const dashboard = page({ 'New provider': button })
  const fresh = [click('link', 'Models'), click('menu', 'Omnara Owner'), point]
  dashboard.runner.take(history)
  dashboard.runner.take([...history, ...fresh])
  dashboard.runner.take([...history, ...fresh])
  await dashboard.runner.settled()

  expect(dashboard.log).toEqual(['click Models', 'click Omnara Owner', 'find New provider'])
  expect(dashboard.pointer()).toEqual({ element: button, note: 'Start here' })
  expect(dashboard.answers).toEqual([
    `link succeeded: Clicked link "Models". It opened /models.\n\n${outline}`,
    `menu succeeded: Clicked button "Omnara Owner" collapsed.\n\n${outline}`,
  ])
})

it('says why nothing was clicked, and leaves a call Omnara stopped waiting for', async () => {
  const dashboard = page({})
  dashboard.runner.take([])
  dashboard.runner.take([
    click('absent', 'Billing'),
    click('off', 'Save'),
    click('late', 'Models', false),
  ])
  await dashboard.runner.settled()

  expect(dashboard.log).toEqual(['click Billing', 'click Save'])
  expect(dashboard.answers).toEqual([
    `absent failed: Nothing to click matching "Billing" is on the user's screen.\n\n${outline}`,
    `off failed: button "Save" disabled is disabled, so nothing was clicked.\n\n${outline}`,
  ])
})

it('treats what arrived while the conversation was hidden or in another tab as history', async () => {
  const dashboard = page({ 'New provider': document.createElement('button') })
  dashboard.runner.take([])
  dashboard.runner.pause()
  dashboard.runner.take([click('hidden', 'Models')])
  dashboard.show(false)
  dashboard.runner.take([click('hidden', 'Models'), point])
  await dashboard.runner.settled()

  expect(dashboard.log).toEqual([])
  expect(dashboard.answers).toEqual([])
})

it('keeps offering its answer until the call is ready for one, and stops on any other refusal', async () => {
  let sends = 0
  await answerWhenReady(
    () => {
      sends++
      return Promise.resolve(sends < 3 ? 'not_ready' : 'sent')
    },
    { timeoutMs: 1000, pollMs: 0 },
  )
  expect(sends).toBe(3)

  const completed = new Error('already completed')
  await expect(
    answerWhenReady(() => Promise.reject(completed), { timeoutMs: 1000, pollMs: 0 }),
  ).rejects.toBe(completed)
})
