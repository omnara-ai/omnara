import { createHash, randomUUID } from 'node:crypto'

import { describe, expect, it } from 'vitest'

import { githubEvent, githubInputKey, githubLifecycleEvent, projectGitHubEvent } from './events'
import { event, nativeComment, webhook } from './inbound-test-support'
import { oldCommit } from './test-support'

describe('GitHub signed event projection', () => {
  it('pins lifecycle delivery identity, original bytes and action without retaining repository inventories', () => {
    const raw = JSON.stringify({
      action: 'removed',
      installation: { id: 123, app_id: 42 },
      repositories_removed: [{ id: 456, irrelevant: 'drop this' }],
    })
    const deliveryID = randomUUID()
    expect(
      githubLifecycleEvent.parse(projectGitHubEvent(raw, 'installation_repositories', deliveryID)),
    ).toEqual({
      delivery_id: deliveryID,
      raw_body_sha256: createHash('sha256').update(raw).digest('hex'),
      event: 'installation_repositories',
      action: 'removed',
      installationID: '123',
      appID: '42',
    })
    expect(() => projectGitHubEvent(raw, 'installation', 'invalid-delivery')).toThrow()
  })
  it('keeps exact native large identities and original content while excluding source/irrelevant objects', () => {
    const raw = JSON.stringify({
      ...webhook,
      action: 'created',
      comment: { ...nativeComment, id: 'LARGE_ID', diff_hunk: 'source-must-not-appear' },
      irrelevant: { secret: 'irrelevant-must-not-appear' },
    }).replace('"LARGE_ID"', '9007199254740993')
    const saved = githubEvent.parse(
      projectGitHubEvent(raw, 'pull_request_review_comment', randomUUID()),
    )
    expect(saved.comment?.id).toBe('9007199254740993')
    expect(saved.comment?.body).toBe(nativeComment.body)
    expect(saved.comment?.user.node_id).toBe(nativeComment.user.node_id)
    expect(saved.sender.node_id).toBe(webhook.sender.node_id)
    expect(saved.raw_body_sha256).toBe(createHash('sha256').update(raw).digest('hex'))
    expect(JSON.stringify(saved)).not.toMatch(/source-must-not-appear|irrelevant-must-not-appear/)
  })
  it('deduplicates communication separately from delivery IDs, while edits/commit transitions stay distinct', () => {
    const body = { ...webhook, action: 'created', comment: nativeComment }
    const first = event(body, 'pull_request_review_comment')
    const duplicate = event(body, 'pull_request_review_comment')
    expect(first.delivery_id).not.toBe(duplicate.delivery_id)
    expect(githubInputKey(first)).toBe(githubInputKey(duplicate))
    expect(
      githubInputKey(event({ ...body, action: 'edited' }, 'pull_request_review_comment')),
    ).not.toBe(githubInputKey(first))
    expect(
      githubInputKey(
        event({ ...webhook, action: 'synchronize', before: oldCommit, after: 'b'.repeat(40) }),
      ),
    ).toContain(`commit:${oldCommit}:`)
  })
  it.each([
    JSON.stringify(webhook).replace('"action":"opened"', '"action":"opened","action":"closed"'),
    JSON.stringify({
      ...webhook,
      pull_request: { ...webhook.pull_request, body: 'x'.repeat(65537) },
    }),
    JSON.stringify({ ...webhook, installation: { id: 'TOO_LARGE' } }).replace(
      '"TOO_LARGE"',
      '9007199254740993',
    ),
    JSON.stringify({ ...webhook, action: 'synchronize' }),
  ])('rejects ambiguous/unrepresentable input without truncating', (raw) => {
    expect(() => projectGitHubEvent(raw, 'pull_request', randomUUID())).toThrow()
  })
  it('does not confuse ordinary issues with PR conversations', () => {
    const { pull_request: issue, ...rest } = webhook
    expect(
      projectGitHubEvent(
        JSON.stringify({ ...rest, issue, action: 'created', comment: nativeComment }),
        'issue_comment',
        randomUUID(),
      ),
    ).toBeUndefined()
    expect(
      event(
        {
          ...rest,
          issue: { ...issue, pull_request: { url: 'https://untrusted.invalid' } },
          action: 'created',
          comment: nativeComment,
        },
        'issue_comment',
      ).comment?.body,
    ).toBe(nativeComment.body)
  })
})
