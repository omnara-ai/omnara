import { describe, expect, it } from 'vitest'

import { SlackClient } from './client'
import type { SlackInboundEvent } from './events'
import { applySlackInputEffects } from './input-effects'
import { attempt, body, credentials, deferred, json, slackServer } from './test-support'

const event: SlackInboundEvent = {
  type: 'message',
  subtype: '',
  user: 'U1',
  bot_id: '',
  text: 'new reply',
  channel: 'C1',
  channel_type: 'channel',
  ts: '100.000010',
  thread_ts: '100.000001',
  team: '',
  source_team: '',
  user_team: '',
  files: [],
}
const canceledID = 'int_aaaaaaaaaaaaaaaaaaaaaaaaae'

describe('Slack post-admission UI effects', () => {
  it('treats already_reacted as success and refreshes only canceled prompt copies', async () => {
    const requests: string[] = []
    const updates: unknown[] = []
    const url = await slackServer((request, response) => {
      requests.push(request.url ?? '')
      void body(request).then((bytes) => {
        const payload: unknown = JSON.parse(bytes.toString())
        if (request.url === '/reactions.add') {
          expect(payload).toEqual({ channel: 'C1', timestamp: event.ts, name: 'eyes' })
          json(response, { ok: false, error: 'already_reacted' })
        } else if (request.url === '/conversations.replies') {
          expect(payload).toEqual({
            channel: 'C1',
            latest: event.ts,
            inclusive: false,
            limit: 15,
            ts: event.thread_ts,
          })
          json(response, {
            ok: true,
            messages: [
              {
                ts: '100.000002',
                text: 'Permission required',
                blocks: [{ type: 'section', block_id: `omnara_interaction_${canceledID}` }],
              },
              {
                ts: '100.000003',
                text: 'Unrelated active prompt',
                blocks: [
                  {
                    type: 'section',
                    block_id: 'omnara_interaction_int_aaaaaaaaaaaaaaaaaaaaaaaabi',
                  },
                ],
              },
              {
                ts: '100.000004',
                text: 'Question',
                blocks: [
                  {
                    type: 'actions',
                    elements: [
                      {
                        action_id: 'omnara_interaction_legacy',
                        value: JSON.stringify({
                          type: 'omnara_interaction',
                          interaction_id: canceledID,
                          agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaae',
                          integration_target_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaae',
                        }),
                      },
                    ],
                  },
                ],
              },
              {
                ts: '100.000005',
                text: 'Malformed unrelated action',
                blocks: [{ elements: [{ action_id: 'omnara_interaction', value: '{' }] }],
              },
            ],
          })
        } else {
          updates.push(payload)
          json(response, { ok: true, ts: '100.000002' })
        }
      })
    })
    await applySlackInputEffects(
      new SlackClient(credentials.botToken, url),
      event,
      [canceledID],
      attempt(),
    )
    expect(requests.filter((path) => path === '/reactions.add')).toHaveLength(1)
    expect(updates).toEqual(
      ['Permission required', 'Question'].map((label, index) => ({
        channel: 'C1',
        ts: index === 0 ? '100.000002' : '100.000004',
        as_user: true,
        text: `${label}\nDismissed because a newer message was sent.`,
        blocks: [
          { type: 'section', text: { type: 'plain_text', text: label } },
          {
            type: 'context',
            elements: [{ type: 'plain_text', text: 'Dismissed because a newer message was sent.' }],
          },
        ],
      })),
    )
  })

  it('uses conversation history for a DM and continues past a deleted prompt', async () => {
    let updates = 0
    const url = await slackServer((request, response) => {
      if (request.url === '/reactions.add') json(response, { ok: true })
      else if (request.url === '/conversations.history')
        json(response, {
          ok: true,
          messages: [
            {
              ts: '100.000002',
              text: 'First',
              blocks: [{ block_id: `omnara_interaction_${canceledID}` }],
            },
            {
              ts: '100.000003',
              text: 'Second',
              blocks: [{ block_id: `omnara_interaction_${canceledID}` }],
            },
          ],
        })
      else {
        updates += 1
        if (updates === 1) json(response, { ok: false, error: 'message_not_found' })
        else json(response, { ok: true, ts: '100.000003' })
      }
    })
    await applySlackInputEffects(
      new SlackClient(credentials.botToken, url),
      { ...event, channel: 'D1', channel_type: 'im', thread_ts: undefined },
      [canceledID],
      attempt(),
    )
    expect(updates).toBe(2)
  })

  it.each([429, 503])(
    'does not retry cosmetic failures or reject admitted input: %s',
    async (status) => {
      const paths: string[] = []
      const url = await slackServer((request, response) => {
        paths.push(request.url ?? '')
        json(response, {}, status, { 'retry-after': '30' })
      })
      await expect(
        applySlackInputEffects(
          new SlackClient(credentials.botToken, url),
          event,
          [canceledID],
          attempt(),
        ),
      ).resolves.toBeUndefined()
      expect(paths.sort()).toEqual(['/conversations.replies', '/reactions.add'])
    },
  )

  it('skips history without canceled IDs and aborts a slow cosmetic request', async () => {
    const closed = deferred()
    const paths: string[] = []
    const url = await slackServer((request, response) => {
      paths.push(request.url ?? '')
      response.writeHead(200, { 'content-type': 'application/json' })
      response.write('{')
      response.on('close', closed.resolve)
    })
    await expect(
      applySlackInputEffects(
        new SlackClient(credentials.botToken, url),
        event,
        [],
        attempt(undefined, 50),
      ),
    ).resolves.toBeUndefined()
    await closed.promise
    expect(paths).toEqual(['/reactions.add'])
  })

  it('never starts UI effects after cancellation', async () => {
    let calls = 0
    const url = await slackServer((_request, response) => {
      calls += 1
      json(response, { ok: true })
    })
    await applySlackInputEffects(
      new SlackClient(credentials.botToken, url),
      event,
      [canceledID],
      attempt(AbortSignal.abort()),
    )
    expect(calls).toBe(0)
  })
})
