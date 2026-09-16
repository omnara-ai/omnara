import { describe, expect, it } from 'vitest'

import { sendGitHubOperation } from './operations'
import { input, operationFixture, options, scope } from './operations-test-support'
import { finding } from './test-support'

describe.each(['inline', 'reply'] as const)(
  'GitHub %s REST acknowledgment projection',
  (action) => {
    it('uses the opaque node ID despite unused unsafe numeric IDs, then verifies publication', async () => {
      const id = action === 'inline' ? finding.id : 'PRRC_reply'
      const f = await operationFixture({
        rest: (_call, response) => {
          response.writeHead(201, { 'content-type': 'application/json' })
          // Literal JSON preserves the actual wire integers; JSON.stringify of JS
          // numbers would round them before the client exercised this boundary.
          response.end(
            `{"node_id":"${id}","id":9007199254740993,"review_id":9007199254740995,` +
              '"pull_request_review_id":9007199254740997,"user":{"id":9007199254740999}}',
          )
          return true
        },
      })
      const result = await sendGitHubOperation(
        f.client,
        action === 'inline' ? input : f.threadInput,
        scope,
        options(),
        f.publishDefinition,
      )
      expect(result).toMatchObject({ publication: 'published', message_id: id })
      const writes = f.calls.filter((call) => call.path.includes('/pulls/7/comments'))
      expect(writes).toHaveLength(1)
      expect(writes[0]?.path).toBe(
        action === 'inline'
          ? '/repos/new-owner/renamed/pulls/7/comments'
          : '/repos/new-owner/renamed/pulls/7/comments/9007199254740993/replies',
      )
      const readbacks = f.calls.filter((call) =>
        JSON.stringify(call.body ?? null).includes('GitHubCommentIdentity'),
      )
      expect(readbacks).toHaveLength(1)
      expect(readbacks[0]?.body).toMatchObject({ variables: { comment: id } })
      expect(JSON.stringify(result)).not.toContain('900719925474099')
    })

    it.each([
      ['missing node ID', '{"id":9007199254740993}'],
      ['null node ID', '{"node_id":null}'],
      ['numeric node ID', '{"node_id":123}'],
      ['unsafe numeric node ID', '{"node_id":9007199254740993}'],
      ['object node ID', '{"node_id":{"id":"PRRC_1"}}'],
      ['duplicate node ID', '{"node_id":"PRRC_1","node_id":"PRRC_reply"}'],
      ['escaped duplicate node ID', '{"node_id":"PRRC_1","node_\\u0069d":"PRRC_reply"}'],
      ['duplicate ignored member', '{"node_id":"PRRC_1","user":{"id":1,"id":2}}'],
      ['malformed ignored member', '{"node_id":"PRRC_1","user":{"id":}}'],
      ['trailing invalid JSON', '{"node_id":"PRRC_1"} false'],
    ])('keeps %s unknown without readback or another POST', async (_label, raw) => {
      const f = await operationFixture({
        rest: (_call, response) => {
          response.writeHead(201, { 'content-type': 'application/json' })
          response.end(raw)
          return true
        },
      })
      await expect(
        sendGitHubOperation(
          f.client,
          action === 'inline' ? input : f.threadInput,
          scope,
          options(),
          f.publishDefinition,
        ),
      ).rejects.toMatchObject({ outcomeUnknown: true, attempts: 1 })
      expect(f.calls.filter((call) => call.path.includes('/pulls/7/comments'))).toHaveLength(1)
      expect(
        f.calls.some((call) => JSON.stringify(call.body ?? null).includes('GitHubCommentIdentity')),
      ).toBe(false)
    })
  },
)
