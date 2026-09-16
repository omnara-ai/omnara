import type {
  LookupChannelConnectorGitHubReviewsRequest,
  RecordChannelConnectorGitHubReviewRequest,
} from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { CoreClient } from './core-client'

const installation = {
  integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  integration_install_id: 'iin_aaaaaaaaaaaaaaaaaaaaaaaaaa',
}
const scope = {
  request_id: 'tcl_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
}
const pinnedCommit = 'a'.repeat(40)
const ownReview = {
  review_id: 'PRR_own',
  ownership: 'owned',
  creating_tool_call_id: scope.request_id,
  commit_id: pinnedCommit,
}

describe('GitHub review identity callbacks', () => {
  it.each([undefined, 'b'.repeat(40)])(
    'retains the creator pin when GitHub reports commit %s',
    async (nativeCommit) => {
      const request: LookupChannelConnectorGitHubReviewsRequest = {
        scope,
        observations: [{ review_id: ownReview.review_id, commit_id: nativeCommit }],
      }
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockResolvedValue(Response.json({ observations: [ownReview] }))
      const result = await client(fetch).lookupGitHubReviews(installation, request)
      expect(result.observations).toEqual([ownReview])
      const sent = requestAt(fetch, 0)
      expect(sent.url).toBe(endpoint('lookup'))
      expect(sent.headers.get('authorization')).toBe('Bearer private-connector-token')
      expect(sent.redirect).toBe('error')
      expect(await sent.json()).toEqual(JSON.parse(JSON.stringify(request)))
    },
  )

  it.each(
    [
      [],
      [ownReview, ownReview],
      [{ ...ownReview, review_id: 'PRR_unrequested' }],
      [{ ...ownReview, ownership: 'future_owner' }],
      [{ ...ownReview, creating_tool_call_id: undefined }],
      [{ ...ownReview, creating_tool_call_id: 'invalid' }],
      [{ ...ownReview, commit_id: undefined }],
      [{ ...ownReview, commit_id: 'invalid' }],
      [{ ...ownReview, ownership: 'other_agent' }],
      [{ ...ownReview, ownership: 'unknown' }],
    ].map((observations) => ({ observations })),
  )(
    'rejects uncorrelated or incomplete ownership before provider dispatch: %j',
    async ({ observations }) => {
      const fetch = vi
        .fn<typeof globalThis.fetch>()
        .mockResolvedValue(Response.json({ observations }))
      await expect(
        client(fetch).lookupGitHubReviews(installation, {
          scope,
          observations: [{ review_id: ownReview.review_id }],
        }),
      ).rejects.toThrow()
      expect(fetch).toHaveBeenCalledOnce()
    },
  )

  it('accepts foreign and unknown identities without disclosing their creator references', async () => {
    const observations = [
      { review_id: 'PRR_foreign', ownership: 'other_agent' },
      { review_id: 'PRR_unknown', ownership: 'unknown' },
    ]
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(Response.json({ observations }))
    expect(
      await client(fetch).lookupGitHubReviews(installation, {
        scope,
        observations: observations.map(({ review_id }) => ({ review_id })),
      }),
    ).toEqual({ observations })
  })

  it('rejects reordered results even when all requested identities are present', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      Response.json({
        observations: [{ review_id: 'PRR_unknown', ownership: 'unknown' }, ownReview],
      }),
    )
    await expect(
      client(fetch).lookupGitHubReviews(installation, {
        scope,
        observations: [{ review_id: ownReview.review_id }, { review_id: 'PRR_unknown' }],
      }),
    ).rejects.toThrow('Invalid GitHub review ownership response')
  })

  it('retries a lost factual acknowledgment with the identical creator identity', async () => {
    const request: RecordChannelConnectorGitHubReviewRequest = {
      scope,
      observation: {
        review_id: ownReview.review_id,
        commit_id: pinnedCommit,
        creating_tool_call_id: scope.request_id,
      },
      evidence: 'create_response',
    }
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockRejectedValueOnce(new TypeError('connection closed after recording'))
      .mockResolvedValueOnce(Response.json({ recorded: true, continue: false }))
    // A stopped creator's identity survives. A successful acknowledgment must
    // not turn continue=false into permission for another provider mutation.
    expect(await client(fetch).recordGitHubReview(installation, request)).toEqual({
      recorded: true,
      continue: false,
    })
    expect(fetch).toHaveBeenCalledTimes(2)
    for (const index of [0, 1]) {
      const sent = requestAt(fetch, index)
      expect(sent.url).toBe(endpoint('record'))
      expect(await sent.json()).toEqual(request)
    }
  })

  it.each([
    { recorded: false, continue: true },
    { recorded: true },
    { recorded: true, continue: 'true' },
  ])('rejects a malformed recording acknowledgment: %j', async (response) => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(response))
    await expect(
      client(fetch).recordGitHubReview(installation, {
        scope,
        observation: { review_id: ownReview.review_id, creating_tool_call_id: scope.request_id },
        evidence: 'marker',
      }),
    ).rejects.toThrow()
    expect(fetch).toHaveBeenCalledOnce()
  })

  it('does not retry an identity conflict', async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(Response.json({ error: 'review identity conflict' }, { status: 409 }))
    await expect(
      client(fetch).recordGitHubReview(installation, {
        scope,
        observation: { review_id: ownReview.review_id, creating_tool_call_id: scope.request_id },
        evidence: 'marker',
      }),
    ).rejects.toThrow()
    expect(fetch).toHaveBeenCalledOnce()
  })
})

function client(fetch: typeof globalThis.fetch) {
  return new CoreClient({
    baseUrl: 'https://core.example.test/api/v1',
    token: 'private-connector-token',
    random: () => 0,
    fetch,
  })
}

function endpoint(operation: 'lookup' | 'record') {
  return `https://core.example.test/api/v1/channel-connector/apps/${installation.integration_app_id}/installations/${installation.integration_install_id}/github-reviews/${operation}`
}

function requestAt(fetch: ReturnType<typeof vi.fn<typeof globalThis.fetch>>, index: number) {
  const input = fetch.mock.calls[index]?.[0]
  if (!(input instanceof Request)) throw new Error('Expected a generated SDK Request')
  return input
}
