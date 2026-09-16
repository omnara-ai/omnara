import type { ServerResponse } from 'node:http'

import type { ChannelSendOperation } from '@omnara/sdk'
import { vi } from 'vitest'

import type { CoreClient } from '../core-client'
import { githubReplyDestination } from './address'
import {
  type APICall,
  comment,
  configuration,
  finding,
  githubFixture,
  type GraphRequest,
  json,
  noPrevious,
  oldCommit,
  pr,
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
  params: { commit_id: oldCommit, path: 'src/main.ts', line: 12, side: 'RIGHT' },
  reply_channel_grants: { receive: false, read: true, send: true },
}
export function options(signal?: AbortSignal) {
  return { requestId: requestID, deadlineMs: Date.now() + 10_000, signal }
}

export async function operationFixture(
  settings: {
    pending?: boolean
    native?: (request: GraphRequest, response: ServerResponse) => boolean | Promise<boolean>
    rest?: (call: APICall, response: ServerResponse) => boolean | Promise<boolean>
  } = {},
) {
  const events: string[] = []
  const publishDefinition = vi
    .fn<CoreClient['publishDefinition']>()
    .mockImplementation((_scope, body) => {
      events.push('definition')
      return Promise.resolve({ ...body, id: 'cdef_aaaaaaaaaaaaaaaaaaaaaaaaaa' })
    })
  const fixture = await githubFixture(
    async (request, response) => {
      if (await settings.native?.(request, response)) return
      if (request.query.includes('GitHubViewer'))
        json(response, { data: { viewer: { id: 'U_bot', login: 'example[bot]' } } })
      else if (request.query.includes('GitHubPendingReviews'))
        json(response, {
          data: {
            node: {
              id: 'R_selected',
              pullRequest: {
                ...pr,
                reviews: { nodes: settings.pending ? [{ id: 'PRR_private' }] : [] },
              },
            },
          },
        })
      else if (request.query.includes('GitHubThreadIdentity'))
        json(response, { data: { node: thread } })
      else if (request.query.includes('GitHubReviewThread('))
        json(response, {
          data: {
            node: {
              ...thread,
              comments: {
                nodes: [finding],
                pageInfo: noPrevious,
              },
            },
          },
        })
      else if (
        request.query.includes('GitHubCommentIdentity') ||
        request.query.includes('GitHubInboundComment')
      )
        json(response, {
          data: {
            node: {
              ...finding,
              id: request.variables.comment ?? finding.id,
              pullRequest: pr,
              replyTo: request.variables.comment === 'PRRC_reply' ? { id: finding.id } : null,
            },
          },
        })
      else if (request.query.includes('GitHubInboundThreads'))
        json(response, {
          data: {
            node: {
              id: 'R_selected',
              pullRequest: {
                ...pr,
                reviewThreads: {
                  nodes: [thread],
                  pageInfo: { hasNextPage: false, endCursor: null },
                },
              },
            },
          },
        })
      else if (request.query.includes('GitHubTimelineComment')) {
        events.push('timeline')
        json(response, { data: { addComment: { commentEdge: { node: comment } } } })
      } else throw new Error('unhandled native request')
    },
    async (call, response) => {
      events.push(call.path.endsWith('/replies') ? 'reply' : 'comment')
      if (await settings.rest?.(call, response)) return
      if (call.path === '/repos/new-owner/renamed/pulls/7/comments')
        json(response, { node_id: finding.id }, 201)
      else if (
        call.path === `/repos/new-owner/renamed/pulls/7/comments/${finding.fullDatabaseId}/replies`
      )
        json(response, { node_id: 'PRRC_reply' }, 201)
      else throw new Error('unexpected native mutation')
    },
  )
  const child = githubReplyDestination(fixture.client, 7, finding.id, thread.id)
  const threadInput: ChannelSendOperation = {
    ...input,
    params: {},
    destination: { ...child, provider_metadata: child.provider_metadata ?? {} },
  }
  return { ...fixture, events, publishDefinition, threadInput }
}
