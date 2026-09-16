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
    for (const update of updates) await f.run(receipt(update))
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
    await f.run(
      receipt(
        event(
          { ...webhook, action: 'created', comment: nativeComment },
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
