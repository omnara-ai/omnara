import type { JsonBody } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { ReceiptBehaviorError } from '../types'
import { WorkByteBudget } from '../work-budget'
import { processGitHubEvent } from './behavior'
import { githubInputKey } from './events'
import {
  contexts,
  coreFixture,
  event,
  nativeComment,
  nativeFixture,
  receipt,
  webhook,
} from './inbound-test-support'
import { mutationInputs, oldCommit } from './test-support'

async function fixture(options?: Parameters<typeof nativeFixture>[0]) {
  const native = await nativeFixture(options)
  const { budget, controller } = contexts()
  const core = coreFixture()
  return {
    ...native,
    budget,
    core,
    run: (queued = receipt()) =>
      processGitHubEvent(
        queued,
        {
          signal: controller.signal,
          deadlineMs: Date.now() + 15_000,
        },
        { core, apiUrl: native.url, reserveWorkBytes: budget.reserve },
      ),
  }
}

describe('GitHub durable PR workflow behavior', () => {
  it('delivers pure PR input without reserving a native-response budget', async () => {
    const core = coreFixture()
    const native = await nativeFixture()
    const budget = new WorkByteBudget(1024 * 1024)
    await processGitHubEvent(
      receipt(),
      { signal: new AbortController().signal, deadlineMs: Date.now() + 10_000 },
      { core, apiUrl: native.url, reserveWorkBytes: budget.reserve },
    )
    expect(core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(native.calls).toEqual([])
    expect(budget.usedBytes).toBe(0)
  })

  it('uses one PR instance for opening, commit updates, timeline and review comments', async () => {
    const f = await fixture()
    const updates = [
      event(),
      event({ ...webhook, action: 'synchronize', before: oldCommit, after: 'b'.repeat(40) }),
      event(
        {
          ...webhook,
          issue: { ...webhook.pull_request, pull_request: {} },
          comment: nativeComment,
          action: 'created',
        },
        'issue_comment',
      ),
      event(
        { ...webhook, action: 'created', comment: nativeComment },
        'pull_request_review_comment',
      ),
    ]
    for (const update of updates) {
      await f.run(receipt(update))
      f.core.lookupWorkflow.mockResolvedValue({
        exists: true,
        agent_state: 'active',
        input_keys: [],
      })
    }
    expect(f.core.deliverWorkflow).toHaveBeenCalledTimes(4)
    const inputs = f.core.deliverWorkflow.mock.calls.map(([, input]) => input)
    expect(new Set(inputs.map((input) => input.instance_key))).toEqual(new Set(['repo:456:pr:7']))
    expect(new Set(inputs.map((input) => input.input_key)).size).toBe(4)
    expect(inputs[3]).toMatchObject({
      target: {
        provider_ref: 'repo:456:pr:7:comment:PRRC_1',
        provider_metadata: { thread_id: 'PRRT_1' },
        parent: { provider_ref: 'repo:456:pr:7' },
      },
      grants: { read: true, send: true },
      metadata: { thread_status: 'published' },
    })
    expect(JSON.stringify(inputs[3]?.content_blocks)).toContain('Original signed finding')
    expect(JSON.stringify(inputs[3]?.content_blocks)).toContain(oldCommit)
    expect(mutationInputs(f.calls)).toEqual([])
    expect(f.budget.usedBytes).toBe(0)
  })
  it('still delivers commit observations when the app itself pushed the change', async () => {
    const f = await fixture()
    f.core.lookupWorkflow.mockResolvedValue({ exists: true, agent_state: 'active', input_keys: [] })
    await f.run(
      receipt(
        event({
          ...webhook,
          action: 'synchronize',
          before: oldCommit,
          after: 'b'.repeat(40),
          sender: { ...webhook.sender, login: 'example[bot]' },
        }),
      ),
    )
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(f.calls).toEqual([])
    expect(mutationInputs(f.calls)).toEqual([])
  })
  it('admits a review comment first with inline PR parent, without a preexisting target', async () => {
    const f = await fixture()
    activation(f, 'mention')
    await f.run(
      receipt(
        event(
          {
            ...webhook,
            action: 'created',
            comment: { ...nativeComment, body: '@example[bot] review this' },
          },
          'pull_request_review_comment',
        ),
      ),
    )
    expect(f.core.lookupWorkflow).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ instance_key: 'repo:456:pr:7' }),
      expect.any(AbortSignal),
    )
    expect(f.core.deliverWorkflow.mock.calls[0]?.[1].target.parent).toMatchObject({
      provider_ref_kind: 'pr',
      definition_id: 'cdef_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    })
    expect(f.core.publishDefinition.mock.calls.map(([, definition]) => definition.kind)).toEqual([
      'GITHUB_PR',
      'GITHUB_REVIEW_THREAD',
    ])
  })
  it.each(['archived', 'replay', 'no route', 'self'])(
    'skips %s without a new input or native mutation',
    async (reason) => {
      const f = await fixture()
      if (reason === 'self') {
        activation(f, 'mention')
        f.core.lookupWorkflow.mockResolvedValue({
          exists: true,
          agent_state: 'active',
          input_keys: [],
        })
      }
      const saved =
        reason === 'self'
          ? event(
              {
                ...webhook,
                action: 'created',
                comment: {
                  ...nativeComment,
                  user: { ...nativeComment.user, node_id: 'U_bot', login: 'example[bot]' },
                },
              },
              'pull_request_review_comment',
            )
          : event()
      if (reason === 'archived')
        f.core.lookupWorkflow.mockResolvedValue({
          exists: true,
          agent_state: 'archived',
          input_keys: [],
        })
      if (reason === 'replay')
        f.core.lookupWorkflow.mockResolvedValue({
          exists: true,
          agent_state: 'active',
          input_keys: [githubInputKey(saved)],
        })
      if (reason === 'no route') f.core.listRoutes.mockResolvedValue([])
      await f.run(receipt(saved))
      expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
      expect(f.core.publishDefinition).not.toHaveBeenCalled()
      if (reason !== 'self') expect(f.calls).toEqual([])
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )
  it.each([{ pending: true }, { foreign: true }])(
    'refuses private or foreign native thread identity: %j',
    async (options) => {
      const f = await fixture(options)
      f.core.lookupWorkflow.mockResolvedValue({
        exists: true,
        agent_state: 'active',
        input_keys: [],
      })
      await expect(
        f.run(
          receipt(
            event(
              { ...webhook, action: 'created', comment: nativeComment },
              'pull_request_review_comment',
            ),
          ),
        ),
      ).rejects.toEqual(new ReceiptBehaviorError(false))
      expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
      expect(mutationInputs(f.calls)).toEqual([])
    },
  )
  it('retains deleted signed comment text on the PR without granting an unverified child', async () => {
    const f = await fixture({ missing: true })
    f.core.lookupWorkflow.mockResolvedValue({ exists: true, agent_state: 'active', input_keys: [] })
    await f.run(
      receipt(
        event(
          { ...webhook, action: 'deleted', comment: nativeComment },
          'pull_request_review_comment',
        ),
      ),
    )
    const delivered = f.core.deliverWorkflow.mock.calls[0]?.[1]
    expect(delivered).toMatchObject({
      target: { provider_ref: 'repo:456:pr:7' },
      metadata: { thread_status: 'unavailable' },
    })
    expect(delivered?.target.parent).toBeUndefined()
    expect(JSON.stringify(delivered?.content_blocks)).toContain(nativeComment.body)
  })
})

function activation(f: Awaited<ReturnType<typeof fixture>>, value: 'pr_open' | 'mention') {
  f.core.listRoutes.mockResolvedValue([
    {
      id: 'iroute_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      behavior_key: 'github_pr',
      configuration: { activation: value },
    },
  ])
}

function communication(kind: string, body = '@example[bot] please review') {
  const [type = '', action = ''] = kind.split(':')
  if (type === 'pull_request') {
    const payload = {
      ...webhook,
      action,
      pull_request: { ...webhook.pull_request, body },
    }
    if (action === 'edited')
      return event({ ...payload, changes: { body: { from: 'Previous description' } } })
    return event(
      action === 'synchronize' ? { ...payload, before: oldCommit, after: 'b'.repeat(40) } : payload,
    )
  }
  if (type === 'pull_request_review')
    return event(
      {
        ...webhook,
        action,
        review: {
          id: 44,
          node_id: 'PRR_1',
          user: webhook.sender,
          body,
          state: action === 'dismissed' ? 'dismissed' : 'commented',
          commit_id: oldCommit,
          submitted_at: '2026-09-15T12:00:00Z',
        },
      },
      type,
    )
  return event(
    {
      ...webhook,
      action,
      issue: { ...webhook.pull_request, pull_request: {} },
      comment: { ...nativeComment, body },
    },
    type,
  )
}

const mentionEvents = [
  'pull_request:opened',
  'pull_request:edited',
  'issue_comment:created',
  'issue_comment:edited',
  'pull_request_review:submitted',
  'pull_request_review:edited',
  'pull_request_review_comment:created',
  'pull_request_review_comment:edited',
]
const nonActivationEvents = [
  'pull_request:synchronize',
  'pull_request:reopened',
  'pull_request:closed',
  'pull_request:ready_for_review',
  'pull_request:converted_to_draft',
  'issue_comment:deleted',
  'pull_request_review:dismissed',
  'pull_request_review_comment:deleted',
]

describe('GitHub initial activation', () => {
  it.each<JsonBody | undefined>([
    { title: { from: 'Old title' } },
    { base: { ref: { from: 'main' }, sha: { from: oldCommit } } },
    undefined,
  ])('does not activate from an unchanged description on a PR edit: %j', async (changes) => {
    const f = await fixture()
    activation(f, 'mention')
    const payload = {
      ...webhook,
      action: 'edited',
      pull_request: { ...webhook.pull_request, body: '@example please review' },
    }
    const saved = event(changes === undefined ? payload : { ...payload, changes })
    await f.run(receipt(saved))
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.core.publishDefinition).not.toHaveBeenCalled()
    expect(f.calls).toEqual([])

    // The same supported edit still reaches an already-active PR agent.
    f.core.lookupWorkflow.mockResolvedValue({ exists: true, agent_state: 'active', input_keys: [] })
    await f.run(receipt(saved))
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
  })
  it.each(
    [...mentionEvents, ...nonActivationEvents].filter((kind) => kind !== 'pull_request:opened'),
  )('ignores %s before automatic PR-open activation, even with a mention', async (kind) => {
    const f = await fixture()
    await f.run(receipt(communication(kind)))
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.core.publishDefinition).not.toHaveBeenCalled()
    expect(f.calls).toEqual([])
    expect(f.budget.usedBytes).toBe(0)
  })
  it.each(mentionEvents)('activates on an exact mention in %s', async (kind) => {
    const f = await fixture()
    activation(f, 'mention')
    await f.run(receipt(communication(kind, 'Please @EXAMPLE[bot], review this.')))
    expect(f.core.deliverWorkflow).toHaveBeenCalledOnce()
    expect(f.core.deliverWorkflow.mock.calls[0]?.[1].instance_key).toBe('repo:456:pr:7')
    expect(mutationInputs(f.calls)).toEqual([])
    expect(f.budget.usedBytes).toBe(0)
  })
  it.each(nonActivationEvents)('ignores stale mentions in %s before activation', async (kind) => {
    const f = await fixture()
    activation(f, 'mention')
    await f.run(receipt(communication(kind)))
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.core.publishDefinition).not.toHaveBeenCalled()
    expect(f.calls).toEqual([])
  })
  it.each([
    'ordinary comment',
    '@example-other',
    '@example[bot]-other',
    '@example[bot]suffix',
    'someone@example[bot]',
    'https://example.com/@example[bot]',
    '@example[bot]/team',
    '@other[bot]',
  ])('does not activate for %s', async (body) => {
    const f = await fixture()
    activation(f, 'mention')
    await f.run(receipt(communication('issue_comment:created', body)))
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.core.publishDefinition).not.toHaveBeenCalled()
  })
  it.each(mentionEvents)('does not activate on the bot’s own mention in %s', async (kind) => {
    const f = await fixture()
    activation(f, 'mention')
    const saved = communication(kind)
    const bot = { ...saved.sender, node_id: 'U_bot', login: 'example[bot]' }
    saved.sender = bot
    if (saved.comment) saved.comment.user = bot
    if (saved.review) saved.review.user = bot
    await f.run(receipt(saved))
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
    expect(f.core.publishDefinition).not.toHaveBeenCalled()
  })
  it.each(['pr_open', 'mention'] as const)(
    'continues an active workflow under %s without another mention',
    async (mode) => {
      const f = await fixture()
      activation(f, mode)
      f.core.lookupWorkflow.mockResolvedValue({
        exists: true,
        agent_state: 'active',
        input_keys: [],
      })
      for (const kind of [...mentionEvents, ...nonActivationEvents]) {
        await f.run(receipt(communication(kind, 'No mention here')))
      }
      expect(f.core.deliverWorkflow).toHaveBeenCalledTimes(
        mentionEvents.length + nonActivationEvents.length,
      )
      expect(
        new Set(f.core.deliverWorkflow.mock.calls.map(([, input]) => input.instance_key)),
      ).toEqual(new Set(['repo:456:pr:7']))
    },
  )
  it('rejects an unknown policy instead of launching', async () => {
    const f = await fixture()
    f.core.listRoutes.mockResolvedValue([
      {
        id: 'iroute_aaaaaaaaaaaaaaaaaaaaaaaaaa',
        behavior_key: 'github_pr',
        configuration: { activation: 'all' },
      },
    ])
    await expect(f.run()).rejects.toEqual(new ReceiptBehaviorError(false))
    expect(f.core.deliverWorkflow).not.toHaveBeenCalled()
  })
})
