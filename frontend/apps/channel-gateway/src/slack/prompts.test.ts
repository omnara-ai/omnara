import { readFileSync } from 'node:fs'

import { schemas } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'
import { z } from 'zod'

import { SlackClient } from './client'
import { renderSlackInteraction, sendSlackInteraction } from './prompts'
import { body, credentials, json, operation, slackPayload, slackServer } from './test-support'

const fixtures = z
  .array(
    z.object({
      name: z.string(),
      operation: schemas.zChannelInteractionOperation,
      selections: z.array(z.array(z.number().int())),
      rendered: z.json(),
      callback: z.json(),
      resolution: schemas.zInteractionResolution,
    }),
  )
  .parse(
    JSON.parse(
      readFileSync(
        new URL(
          '../../../../../internal/integration/slack/testdata/gateway_interactions.json',
          import.meta.url,
        ),
        'utf8',
      ),
    ),
  )

const callbackBlocks = z.array(
  z.discriminatedUnion('type', [
    z.object({ type: z.literal('section') }),
    z.object({
      type: z.literal('actions'),
      elements: z.array(z.object({ action_id: z.string(), value: z.string() })),
    }),
    z.object({
      type: z.literal('input'),
      block_id: z.string(),
      element: z.object({
        type: z.enum(['radio_buttons', 'checkboxes']),
        action_id: z.string(),
        options: z.array(z.object({ value: z.string() })),
      }),
    }),
  ]),
)

describe('Slack canonical interaction presentation', () => {
  it.each(fixtures)('pins TS render and submitted state consumed by Go: $name', (fixture) => {
    const before = JSON.stringify(fixture.operation)
    const rendered = renderSlackInteraction(fixture.operation)
    expect(rendered).toEqual(fixture.rendered)
    // Build the callback from the actual rendered IDs/options, not copied constants.
    expect(submit(rendered, fixture.selections)).toEqual(fixture.callback)
    expect(JSON.stringify(fixture.operation)).toBe(before)
  })

  it('preserves native fallback for forms above Slack block/option limits', () => {
    const base = fixtures[0]
    if (!base) throw new Error('missing shared interaction fixture')
    for (const questions of [
      [
        {
          prompt: 'Choose',
          options: Array.from({ length: 11 }, (_, index) => ({ label: `Choice ${index}` })),
        },
      ],
      Array.from({ length: 49 }, () => ({ prompt: 'Choose', options: [{ label: 'Yes' }] })),
    ]) {
      const rendered = renderSlackInteraction({
        ...base.operation,
        form: {
          title: 'Many choices',
          questions,
        },
      })
      expect(rendered.blocks).toHaveLength(2)
      expect(JSON.stringify(rendered.blocks)).toContain('Respond in Omnara to continue.')
      expect(JSON.stringify(rendered.blocks)).not.toContain('"action_id"')
      expect(rendered.text).toContain('1. Choose')
    }
  })

  it('uses native code-point label limits and leaves canonical long form unchanged', () => {
    const base = fixtures[0]
    if (!base) throw new Error('missing shared interaction fixture')
    const input = {
      ...base.operation,
      form: {
        title: '😀'.repeat(3001),
        questions: [
          { prompt: '😀'.repeat(2001), options: [{ label: '😀'.repeat(76), allows_text: true }] },
        ],
      },
    }
    const rendered = renderSlackInteraction(input)
    expect(Array.from(rendered.text)).toHaveLength(3000)
    expect(rendered.text.endsWith('...')).toBe(true)
    const blocks = z
      .array(
        z.object({
          label: z.object({ text: z.string() }).optional(),
          element: z
            .object({ options: z.array(z.object({ text: z.object({ text: z.string() }) })) })
            .optional(),
        }),
      )
      .parse(rendered.blocks)
    expect(Array.from(blocks[1]?.label?.text ?? '')).toHaveLength(2000)
    expect(Array.from(blocks[1]?.element?.options[0]?.text.text ?? '')).toHaveLength(75)
    expect(input.form.title).toBe('😀'.repeat(3001))
  })

  it('posts the canonical presentation and returns only a presentation result', async () => {
    const fixture = fixtures[0]
    if (!fixture) throw new Error('missing shared interaction fixture')
    let requestBody: unknown
    let calls = 0
    const url = await slackServer((request, response) => {
      calls++
      void body(request).then((bytes) => {
        requestBody = slackPayload(bytes)
        json(response, { ok: true, channel: 'C123', ts: '100.000002' })
      })
    })
    const result = await sendSlackInteraction(
      new SlackClient(credentials.botToken, url),
      fixture.operation,
      operation(),
    )
    expect(requestBody).toEqual(fixture.rendered)
    expect(schemas.zChannelInteractionOperationResult.parse(result)).toEqual({
      message_id: '100.000002',
    })
    expect(calls).toBe(1)
  })

  it('retries a prompt only after explicit non-publication rate-limit evidence', async () => {
    const fixture = fixtures[0]
    if (!fixture) throw new Error('missing shared interaction fixture')
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls += 1
      if (calls === 1) json(response, {}, 429, { 'retry-after': '0' })
      else json(response, { ok: true, channel: 'C123', ts: '100.000002' })
    })
    expect(
      await sendSlackInteraction(
        new SlackClient(credentials.botToken, url),
        fixture.operation,
        operation(),
      ),
    ).toEqual({ message_id: '100.000002' })
    expect(calls).toBe(2)
  })

  it('does not repost a prompt with an unknown publication outcome', async () => {
    const fixture = fixtures[0]
    if (!fixture) throw new Error('missing shared interaction fixture')
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls++
      json(response, { ok: false, error: 'internal_error' })
    })
    await expect(
      sendSlackInteraction(
        new SlackClient(credentials.botToken, url),
        fixture.operation,
        operation(),
      ),
    ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
    expect(calls).toBe(1)
  })
})

function submit(request: ReturnType<typeof renderSlackInteraction>, selections: number[][]) {
  const blocks = callbackBlocks.parse(request.blocks)
  const actions = blocks.flatMap((block) => (block.type === 'actions' ? block.elements : []))
  const values: Record<
    string,
    Record<
      string,
      {
        selected_option?: { value: string }
        selected_options?: { value: string }[]
      }
    >
  > = {}
  let index = 0
  for (const block of blocks) {
    if (block.type !== 'input') continue
    const selected = (selections[index++] ?? []).map((optionIndex) => {
      const option = block.element.options[optionIndex]
      if (!option) throw new Error('fixture selection missing from rendered options')
      return option
    })
    const first = selected[0]
    if (!first) throw new Error('fixture question requires a selection')
    values[block.block_id] = {
      [block.element.action_id]:
        block.element.type === 'checkboxes'
          ? { selected_options: selected }
          : { selected_option: first },
    }
  }
  return {
    type: 'block_actions',
    api_app_id: 'A123',
    team: { id: 'T123' },
    user: { id: 'U123', team_id: 'T123' },
    actions,
    state: { values },
  }
}
