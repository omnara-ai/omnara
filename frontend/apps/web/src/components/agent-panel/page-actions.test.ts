/** @vitest-environment happy-dom */

import type { OmnaraUIMessage } from '@omnara/react'
import { expect, it } from 'vitest'

import {
  createPageActionRunner,
  type PageAction,
  type PagePointer,
  pendingPageActions,
} from './page-actions'

function call(
  id: string,
  toolName: string,
  input: Record<string, string>,
  options: { answered?: boolean; custom?: boolean } = {},
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
        ...(options.answered === true
          ? { state: 'output-available', output: { contentBlocks: [] } }
          : { state: 'input-available' }),
      },
    ],
  }
}

it('reads the page tool calls still waiting for an answer, in order', () => {
  expect(
    pendingPageActions([
      call('done', 'dashboard_open_page', { path: '/usage' }, { answered: true }),
      call('one', 'dashboard_open_page', { path: '/models' }),
      call('two', 'dashboard_point_at', { target: 'New model', note: 'Start here' }),
      call('empty', 'dashboard_point_at', { target: '' }),
      call('mcp', 'dashboard_open_page', { path: '/usage' }, { custom: false }),
      call('other', 'create_ticket', { path: '/models' }),
    ]),
  ).toEqual([
    { id: 'one', tool: 'dashboard_open_page', path: '/models' },
    { id: 'two', tool: 'dashboard_point_at', target: 'New model', note: 'Start here' },
  ])
})

function page(found: Record<string, Element>) {
  const log: string[] = []
  const answers: string[] = []
  let pointer: PagePointer | null = null
  let visible = true
  const runner = createPageActionRunner({
    visible: () => visible,
    open: (path) => {
      log.push(`open ${path}`)
      return Promise.resolve(path.startsWith('/models'))
    },
    find: (target) => {
      log.push(`find ${target}`)
      return Promise.resolve(found[target] ?? null)
    },
    point: (next) => {
      pointer = next
    },
    snapshot: () => '# Models\nbutton "New provider"',
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

const open: PageAction = { id: 'open', tool: 'dashboard_open_page', path: '/models' }
const point: PageAction = {
  id: 'point',
  tool: 'dashboard_point_at',
  target: 'New provider',
  note: 'Start here',
}

it('answers each waiting call once, in order, with what the user now sees', async () => {
  const button = document.createElement('button')
  const dashboard = page({ 'New provider': button })
  dashboard.runner.observe([open, point])
  dashboard.runner.observe([open, point])
  await dashboard.runner.settled()

  expect(dashboard.log).toEqual(['open /models', 'find New provider'])
  expect(dashboard.pointer()).toEqual({ element: button, note: 'Start here' })
  expect(dashboard.answers).toEqual([
    'open succeeded: Opened /models.\n\nWhat the user sees now:\n# Models\nbutton "New provider"',
    'point succeeded: The user can see it marked on the page.',
  ])
})

it('tells the assistant when a page or a target is not there', async () => {
  const dashboard = page({})
  dashboard.runner.observe([{ id: 'api', tool: 'dashboard_open_page', path: '/api/v1/me' }, point])
  await dashboard.runner.settled()

  expect(dashboard.pointer()).toBeNull()
  expect(dashboard.answers).toEqual([
    'api failed: /api/v1/me is not a dashboard page.',
    'point failed: Nothing on the page is named "New provider".\n\nWhat the user sees now:\n# Models\nbutton "New provider"',
  ])
})

it('leaves calls for a tab the user is looking at, and takes them once this one is', async () => {
  const dashboard = page({})
  dashboard.show(false)
  dashboard.runner.observe([open])
  await dashboard.runner.settled()
  expect(dashboard.answers).toEqual([])

  dashboard.show(true)
  dashboard.runner.observe([open])
  await dashboard.runner.settled()
  expect(dashboard.answers).toHaveLength(1)
})
