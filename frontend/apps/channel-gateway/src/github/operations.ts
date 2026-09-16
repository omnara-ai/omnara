import {
  type ChannelOpaqueObject,
  type ChannelReadOperation,
  type ChannelReadOperationResult,
  type ChannelSendOperation,
  type ChannelSendOperationResult,
  schemas,
} from '@omnara/sdk'

import type { CoreClient } from '../core-client'
import { isTransientCoreError } from '../core-http'
import { type OperationRetryOptions, retryOperation } from '../operation-retry'
import type { OperationScope } from '../operations'
import { githubAddress, githubReplyDestination } from './address'
import { githubDefinition } from './behavior'
import type { GitHubClient } from './client'
import { findGitHubReviewThread } from './inbound-thread'
import {
  getGitHubReviewThread,
  type GitHubMessage,
  postGitHubInlineComment,
  postGitHubTimelineComment,
  replyToGitHubReviewThread,
} from './messages'
import {
  GitHubAPIError,
  type GitHubFinding,
  type GitHubPRParams,
  parseGitHubParams,
  validateGitHubText,
} from './protocol'
import { readGitHubHistoryAttempt } from './read'

type RetryOptions = Omit<OperationRetryOptions, 'idempotent'>

function validateScope(client: GitHubClient, scope: OperationScope, requestID: string): void {
  if (
    scope.project_id !== client.configuration.projectID ||
    scope.integration_install_id !== client.configuration.integrationInstallID ||
    !schemas.zToolCallId.safeParse(requestID).success ||
    !schemas.zAgentId.safeParse(scope.agent_id).success ||
    !schemas.zIntegrationTargetId.safeParse(scope.channel_id).success
  )
    throw new GitHubAPIError('invalid_request')
}

function messageResult(message: GitHubMessage): ChannelSendOperationResult {
  return {
    publication: message.publication,
    message_channel: 'destination',
    message_id: message.id,
    created_at: message.createdAt,
    metadata: messageMetadata(message),
  }
}

function messageMetadata(message: GitHubMessage): ChannelOpaqueObject {
  const metadata: ChannelOpaqueObject = {}
  if (message.commitID) metadata.commit_id = message.commitID
  if (message.path) metadata.path = message.path
  if (message.line !== undefined) metadata.line = message.line
  return metadata
}

/** Core already admitted the actual persisted arguments/runtime/binding. Native
 * comments are single explicit publications with no provider-specific core state.
 */
export async function sendGitHubOperation(
  client: GitHubClient,
  input: ChannelSendOperation,
  scope: OperationScope,
  options: RetryOptions,
  publishDefinition: CoreClient['publishDefinition'],
): Promise<ChannelSendOperationResult> {
  return retryOperation(options, async (context) => {
    validateScope(client, scope, options.requestId)
    const address = githubAddress(client, input.destination)
    const params = parseGitHubParams(
      JSON.stringify(input.params),
      address.threadID ? 'review_thread' : 'pr',
    )
    if (input.message.artifact_ids?.length || input.message.text === undefined)
      throw new GitHubAPIError('invalid_message')
    const text = input.message.text
    validateGitHubText(text)
    if (address.threadID && address.rootID)
      return messageResult(
        await replyToGitHubReviewThread(
          client,
          address.number,
          address.threadID,
          address.rootID,
          text,
          context,
        ),
      )
    if (!isFinding(params))
      return messageResult(await postGitHubTimelineComment(client, address.number, text, context))

    // A send-only agent can create the first thread before any inbound event.
    // Its generic definition must exist before the native write/child completion.
    if (input.reply_channel_grants) {
      try {
        await publishDefinition(scope, githubDefinition('review_thread'), context.signal)
      } catch (cause) {
        throw new GitHubAPIError('provider_unavailable', { retryable: isTransientCoreError(cause) })
      }
    }
    const message = await postGitHubInlineComment(client, address.number, text, params, context)
    if (!input.reply_channel_grants) return messageResult(message)
    // Publication is known. Resolving/registration failure must not replay it.
    // Core owns child registration and binding grants from this native address.
    try {
      const thread = await findGitHubReviewThread(client, address.number, message.id, context)
      return {
        ...messageResult(message),
        message_channel: 'reply_channel',
        reply_channel: githubReplyDestination(
          client,
          address.number,
          thread.rootID,
          thread.threadID,
        ),
      }
    } catch {
      return {
        ...messageResult(message),
        continuation_error: {
          code: 'reply_channel_unavailable',
          message:
            'The comment was published, but its reply channel could not be resolved. Do not resend it.',
        },
      }
    }
  })
}

function isFinding(params: GitHubPRParams): params is GitHubFinding {
  return 'commit_id' in params
}

export async function readGitHubOperation(
  client: GitHubClient,
  input: ChannelReadOperation,
  scope: OperationScope,
  options: RetryOptions,
): Promise<ChannelReadOperationResult> {
  return retryOperation({ ...options, idempotent: true }, async (context) => {
    validateScope(client, scope, options.requestId)
    const address = githubAddress(client, input.destination)
    if (address.threadID) {
      const thread = await getGitHubReviewThread(client, address.number, address.threadID, context)
      if (thread.comments.nodes[0]?.id !== address.rootID)
        throw new GitHubAPIError('invalid_address')
    }
    const page = await readGitHubHistoryAttempt(
      client,
      { ...address, limit: input.limit, cursor: input.cursor },
      context,
    )
    return {
      messages: page.messages.map((message) => ({
        content: { text: message.text },
        publication: message.publication,
        message_id: message.id,
        created_at: message.createdAt,
        author: message.authorRef ? { ref: message.authorRef } : undefined,
        reply_to: message.replyTo ? { message_id: message.replyTo } : undefined,
        metadata: messageMetadata(message),
      })),
      next_cursor: page.nextCursor,
      coverage: page.coverage,
      coverage_reason: page.reason,
    }
  })
}
