import type { ServerResponse } from 'node:http'

import {
  type ChannelSendOperation,
  type GitHubReviewOwnership,
  type LookupChannelConnectorGitHubReviewsRequest,
  type RecordChannelConnectorGitHubReviewRequest,
  schemas,
} from '@omnara/sdk'

import { CoreClient } from '../core-client'
import { githubReplyDestination } from './address'
import {
  configuration,
  finding,
  githubFixture,
  type GraphRequest,
  json,
  oldCommit,
  pr,
  review,
  thread,
} from './test-support'

export const requestID = 'tcl_aaaaaaaaaaaaaaaaaaaaaaaaaa'
export const scope = {
  project_id: configuration.projectID,
  integration_app_id: 'iapp_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  integration_install_id: configuration.integrationInstallID,
  agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
  channel_id: 'itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
}
export const input: ChannelSendOperation = {
  destination: {
    implementation_key: 'github_pr',
    provider_ref: 'repo:456:pr:7',
    provider_ref_kind: 'pr',
    provider_metadata: {},
  },
  message: { text: finding.body },
  params: {
    review_comment: true,
    commit_id: oldCommit,
    path: 'src/main.ts',
    line: 12,
    side: 'RIGHT',
  },
  reply_channel_grants: { receive: false, read: true, send: true },
}
export function options(signal?: AbortSignal) {
  return { requestId: requestID, deadlineMs: Date.now() + 10_000, signal }
}

export async function operationFixture(
  settings: {
    pending?: (typeof review)[]
    ownership?: GitHubReviewOwnership
    ownershipForReview?: (id: string) => GitHubReviewOwnership
    continue?: boolean
    native?: (request: GraphRequest, response: ServerResponse) => boolean | Promise<boolean>
    rest?: Parameters<typeof githubFixture>[1]
    record?: (request: RecordChannelConnectorGitHubReviewRequest, signal: AbortSignal) => Response
  } = {},
) {
  const events: string[] = []
  const lookups: LookupChannelConnectorGitHubReviewsRequest[] = []
  const records: RecordChannelConnectorGitHubReviewRequest[] = []
  const core = new CoreClient({
    baseUrl: 'https://core.example.test/api/v1',
    token: 'fixture-token',
    random: () => 0,
    fetch: async (raw) => {
      if (!(raw instanceof Request)) throw new Error('expected SDK request')
      const body: unknown = await raw.json()
      if (raw.url.endsWith('/lookup')) {
        const request = schemas.zLookupChannelConnectorGitHubReviewsRequest.parse(body)
        lookups.push(request)
        return Response.json({
          observations: request.observations.map((observation) => {
            const ownership =
              settings.ownershipForReview?.(observation.review_id) ?? settings.ownership ?? 'owned'
            if (ownership === 'owned')
              return {
                review_id: observation.review_id,
                ownership,
                creating_tool_call_id: requestID,
                commit_id: oldCommit,
              }
            return { review_id: observation.review_id, ownership }
          }),
        })
      }
      const request = schemas.zRecordChannelConnectorGitHubReviewRequest.parse(body)
      records.push(request)
      events.push('record')
      return (
        settings.record?.(request, raw.signal) ??
        Response.json({ recorded: true, continue: settings.continue ?? true })
      )
    },
  })
  const fixture = await githubFixture(async (request, response) => {
    if (await settings.native?.(request, response)) return
    if (request.query.includes('GitHubViewer'))
      json(response, { data: { viewer: { login: 'example[bot]' } } })
    else if (request.query.includes('GitHubPendingReviews'))
      json(response, {
        data: {
          node: {
            id: 'R_selected',
            pullRequest: {
              ...pr,
              reviews: {
                nodes: settings.pending ?? [],
                pageInfo: { hasNextPage: false, endCursor: null },
              },
            },
          },
        },
      })
    else if (request.query.includes('GitHubReviewIdentity'))
      json(response, { data: { node: { ...review, pullRequest: pr } } })
    else if (request.query.includes('GitHubThreadIdentity'))
      json(response, { data: { node: thread } })
    else if (request.query.includes('GitHubCreateReview')) {
      events.push('create')
      json(response, { data: { addPullRequestReview: { pullRequestReview: { id: review.id } } } })
    } else if (request.query.includes('GitHubAddFinding')) {
      events.push('finding')
      json(response, { data: { addPullRequestReviewThread: { thread } } })
    } else if (
      request.query.includes('GitHubSubmitReview') ||
      request.query.includes('GitHubReviewSummary')
    ) {
      events.push('submit')
      const field = request.query.includes('GitHubSubmitReview')
        ? 'submitPullRequestReview'
        : 'addPullRequestReview'
      json(response, {
        data: {
          [field]: {
            pullRequestReview: { ...review, state: 'COMMENTED', submittedAt: review.createdAt },
          },
        },
      })
    } else if (request.query.includes('GitHubThreadReply')) {
      events.push('reply')
      json(response, {
        data: {
          addPullRequestReviewThreadReply: {
            comment: { ...finding, id: 'PRRC_reply', replyTo: { id: finding.id } },
          },
        },
      })
    } else if (request.query.includes('GitHubTimelineComment')) {
      events.push('timeline')
      json(response, { data: { addComment: { commentEdge: { node: finding } } } })
    } else throw new Error('unhandled native request')
  }, settings.rest)
  const child = githubReplyDestination(fixture.client, 7, finding.id, thread.id)
  const threadInput: ChannelSendOperation = {
    ...input,
    params: {},
    destination: { ...child, provider_metadata: child.provider_metadata ?? {} },
  }
  return { ...fixture, core, events, lookups, records, threadInput }
}
