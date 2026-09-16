import {
  type ChannelOpaqueObject,
  type ChannelOperationFailure,
  type ChannelReadOperation,
  type ChannelReadOperationResult,
  type ChannelSendOperation,
  type ChannelSendOperationResult,
  schemas,
} from '@omnara/sdk'

import {
  type OperationAttemptContext,
  OperationRetryError,
  type OperationRetryOptions,
  retryOperation,
} from '../operation-retry'
import type { OperationScope } from '../operations'
import { type GitHubAddress, githubAddress, githubReplyDestination } from './address'
import type { GitHubClient } from './client'
import {
  addGitHubReviewFinding,
  createGitHubPendingReview,
  getGitHubReview,
  getGitHubReviewThread,
  type GitHubMessage,
  postGitHubReviewSummary,
  postGitHubTimelineComment,
  replyToGitHubReviewThread,
  submitGitHubReview,
} from './messages'
import {
  GitHubAPIError,
  type GitHubFinding,
  type GitHubPRParams,
  parseGitHubParams,
  validateGitHubText,
} from './protocol'
import { readGitHubHistoryAttempt } from './read'
import {
  type GitHubReviewAuthority,
  type GitHubReviewCore,
  githubReviewMarker,
  lookupGitHubOwnership,
  observedGitHubPending,
  recordGitHubCreation,
} from './reviews'

type RetryOptions = Omit<OperationRetryOptions, 'idempotent'>

/** The outer transport preserves this fixed payload even for an unknown outcome. */
export class GitHubOperationError extends OperationRetryError {
  constructor(
    error: OperationRetryError,
    readonly payload?: ChannelOperationFailure,
  ) {
    super(error.code, error.outcomeUnknown, error.attempts)
  }
}

interface Recovery {
  code?: Exclude<ChannelOperationFailure['code'], 'review_operation_unknown'>
  metadata?: ChannelOperationFailure['metadata']
  reviewAction?: boolean
}

async function run<T>(
  options: RetryOptions,
  recovery: Recovery,
  work: (context: OperationAttemptContext) => Promise<T>,
  read = false,
): Promise<T> {
  try {
    return await retryOperation({ ...options, idempotent: read }, async (context) => {
      try {
        return await work(context)
      } catch (error) {
        if (error instanceof GitHubAPIError) {
          const code = schemas.zChannelOperationFailureCode.safeParse(error.code)
          if (code.success && code.data !== 'review_operation_unknown') recovery.code = code.data
        }
        throw error
      }
    })
  } catch (error) {
    if (!(error instanceof OperationRetryError)) throw error
    let payload: ChannelOperationFailure | undefined
    if (!read && error.outcomeUnknown) {
      if (recovery.reviewAction)
        payload = { code: 'review_operation_unknown', metadata: recovery.metadata }
    } else if (recovery.code) {
      payload = { code: recovery.code }
      if (
        [
          'pending_review_exists',
          'review_commit_mismatch',
          'review_creation_already_recorded',
          'review_finding_failed',
        ].includes(recovery.code)
      )
        payload.metadata = recovery.metadata
    }
    throw new GitHubOperationError(error, payload)
  }
}

function authority(
  client: GitHubClient,
  core: GitHubReviewCore,
  scope: OperationScope,
  options: RetryOptions,
): GitHubReviewAuthority {
  if (
    scope.project_id !== client.configuration.projectID ||
    scope.integration_install_id !== client.configuration.integrationInstallID
  )
    throw new GitHubAPIError('repository_scope_mismatch')
  const parsed = schemas.zGitHubReviewOperationScope.safeParse({
    request_id: options.requestId,
    agent_id: scope.agent_id,
    channel_id: scope.channel_id,
  })
  if (!parsed.success) throw new GitHubAPIError('invalid_request')
  return { core, installation: scope, scope: parsed.data }
}

function messageResult(
  message: GitHubMessage,
  owner?: Recovery['metadata'],
): ChannelSendOperationResult {
  const metadata = messageMetadata(message)
  if (owner?.review_id === message.reviewID && owner?.commit_id)
    metadata.commit_id = owner.commit_id
  return {
    publication: message.publication,
    message_channel: 'destination',
    message_id: message.id,
    created_at: message.createdAt,
    metadata,
  }
}

function messageMetadata(message: GitHubMessage): ChannelOpaqueObject {
  const metadata: ChannelOpaqueObject = {}
  if (message.reviewID) metadata.review_id = message.reviewID
  if (message.commitID) metadata.commit_id = message.commitID
  if (message.path) metadata.path = message.path
  if (message.line !== undefined) metadata.line = message.line
  return metadata
}

async function checkedThread(
  client: GitHubClient,
  address: GitHubAddress,
  context: OperationAttemptContext,
) {
  if (!address.threadID) throw new GitHubAPIError('invalid_address')
  const thread = await getGitHubReviewThread(client, address.number, address.threadID, context)
  const root = thread.comments.nodes[0]
  if (!root || root.id !== address.rootID) throw new GitHubAPIError('invalid_address')
  return root
}

/** Core already admitted the persisted arguments/runtime/binding. This scope is
 * the actual operation envelope, never an invented second authorization call.
 */
export async function sendGitHubOperation(
  client: GitHubClient,
  core: GitHubReviewCore,
  input: ChannelSendOperation,
  scope: OperationScope,
  options: RetryOptions,
): Promise<ChannelSendOperationResult> {
  const recovery: Recovery = {}
  // Successful steps survive safe retries. In particular, a rejected finding
  // must not repeat its successful container creation or identity acknowledgment.
  let created: { id: string; recorded: boolean } | undefined
  let omittedChecked = false
  return run(options, recovery, async (context) => {
    const auth = authority(client, core, scope, options)
    const address = githubAddress(client, input.destination)
    const params = parseGitHubParams(
      JSON.stringify(input.params),
      address.threadID ? 'review_thread' : 'pr',
    )
    if (input.message.artifact_ids?.length || input.message.text === undefined)
      throw new GitHubAPIError('invalid_message')
    const text = input.message.text
    validateGitHubText(text)
    if (address.threadID) {
      recovery.reviewAction = true
      const root = await checkedThread(client, address, context)
      let ownedID: string | undefined
      if (root.state === 'PENDING') {
        const id = root.pullRequestReview?.id
        if (!id) throw new GitHubAPIError('review_state_unavailable')
        const [owner] = await lookupGitHubOwnership(auth, [{ review_id: id }], context)
        if (owner?.ownership !== 'owned') throw new GitHubAPIError('review_not_owned')
        ownedID = id
        recovery.metadata = { review_id: id, commit_id: owner.commit_id }
      }
      return messageResult(
        await replyToGitHubReviewThread(
          client,
          address.number,
          address.threadID,
          text,
          context,
          ownedID,
        ),
        recovery.metadata,
      )
    }
    if (!('review_comment' in params) && !('publish_review' in params))
      return messageResult(await postGitHubTimelineComment(client, address.number, text, context))

    recovery.reviewAction = true
    let reviewID = params.review_id
    if (reviewID) {
      // Lookup is the owner check even when the native commit is null. The core
      // pin is authoritative; a copied marker on an arbitrary review is not used.
      const [owner] = await lookupGitHubOwnership(auth, [{ review_id: reviewID }], context)
      if (owner?.ownership !== 'owned') throw new GitHubAPIError('review_not_owned')
      recovery.metadata = { review_id: reviewID, commit_id: owner.commit_id }
      if (
        'review_comment' in params &&
        params.commit_id.toLowerCase() !== owner.commit_id?.toLowerCase()
      )
        throw new GitHubAPIError('review_commit_mismatch')
      recovery.code = 'review_state_unavailable'
      const review = await getGitHubReview(client, address.number, reviewID, context)
      if (review.state !== 'PENDING') throw new GitHubAPIError('review_state_unavailable')
      recovery.code = undefined
    } else if (!omittedChecked) {
      recovery.code = 'review_state_unavailable'
      const pending = await observedGitHubPending(client, address.number, auth, context)
      if (pending.ownership.some((entry) => entry.ownership !== 'owned'))
        throw new GitHubAPIError('provider_pending_review_conflict')
      const own = pending.ownership.find((entry) => entry.ownership === 'owned')
      if (own) {
        recovery.metadata = { review_id: own.review_id, commit_id: own.commit_id }
        const observation = pending.observations.find((entry) => entry.review_id === own.review_id)
        if (observation?.creating_tool_call_id) {
          // Recover a positively correlated lost acknowledgment. This call still
          // returns the omission guard; recording does not authorize a new send.
          try {
            await core.recordGitHubReview(
              auth.installation,
              {
                scope: auth.scope,
                observation: {
                  ...observation,
                  creating_tool_call_id: observation.creating_tool_call_id,
                },
                evidence: 'marker',
              },
              context.signal,
            )
          } catch {
            throw new GitHubAPIError('review_state_unavailable')
          }
        }
        throw new GitHubAPIError('pending_review_exists')
      }
      if (pending.observations.length) throw new GitHubAPIError('provider_pending_review_conflict')
      omittedChecked = true
      recovery.code = undefined
    }
    if ('publish_review' in params) {
      return messageResult(
        reviewID
          ? await submitGitHubReview(client, address.number, reviewID, text, context)
          : await postGitHubReviewSummary(client, address.number, text, context),
        recovery.metadata,
      )
    }
    if (!isFinding(params)) throw new GitHubAPIError('invalid_params')
    if (!reviewID) {
      if (!created) {
        const result = await createGitHubPendingReview(
          client,
          address.number,
          params.commit_id,
          githubReviewMarker(options.requestId),
          context,
        )
        created = { id: result.id, recorded: false }
        recovery.metadata = { review_id: result.id, commit_id: params.commit_id }
        recovery.code = 'review_finding_failed'
      }
      if (!created.recorded) {
        const mayContinue = await recordGitHubCreation(auth, created.id, params.commit_id, context)
        created.recorded = true
        if (!mayContinue) throw new GitHubAPIError('review_finding_failed')
      }
      reviewID = created.id
    }
    context.signal.throwIfAborted()
    recovery.code = 'review_finding_failed'
    const finding = await addGitHubReviewFinding(
      client,
      address.number,
      reviewID,
      text,
      params,
      context,
    )
    // A known native finding remains a draft success. Core alone registers the
    // child and reports continuation_error if grants/registration are unavailable.
    return {
      ...messageResult(finding.message, recovery.metadata),
      message_channel: 'reply_channel',
      reply_channel: githubReplyDestination(
        client,
        address.number,
        finding.message.id,
        finding.threadID,
      ),
    }
  })
}

export async function readGitHubOperation(
  client: GitHubClient,
  core: GitHubReviewCore,
  input: ChannelReadOperation,
  scope: OperationScope,
  options: RetryOptions,
): Promise<ChannelReadOperationResult> {
  return run(
    options,
    {},
    async (context) => {
      const auth = authority(client, core, scope, options)
      const address = githubAddress(client, input.destination)
      if (address.threadID) await checkedThread(client, address, context)
      const page = await readGitHubHistoryAttempt(
        client,
        { ...address, limit: input.limit, cursor: input.cursor },
        context,
      )
      const ids = [
        ...new Set(
          page.messages
            .filter((message) => message.publication === 'draft')
            .flatMap((message) => (message.reviewID ? [message.reviewID] : [])),
        ),
      ]
      const ownership = ids.length
        ? await lookupGitHubOwnership(
            auth,
            ids.map((id) => ({ review_id: id })),
            context,
          )
        : []
      const owned = new Set(
        ownership.filter((entry) => entry.ownership === 'owned').map((entry) => entry.review_id),
      )
      const visible = page.messages.filter(
        (message) =>
          message.publication !== 'draft' ||
          (message.reviewID !== undefined && owned.has(message.reviewID)),
      )
      const filtered = visible.length !== page.messages.length
      return {
        messages: visible.map((message) => ({
          content: { text: message.text },
          publication: message.publication,
          message_id: message.id,
          created_at: message.createdAt,
          author: message.authorRef ? { ref: message.authorRef } : undefined,
          reply_to: message.replyTo ? { message_id: message.replyTo } : undefined,
          metadata: messageMetadata(message),
        })),
        next_cursor: page.nextCursor,
        coverage: filtered ? 'partial' : page.coverage,
        coverage_reason: filtered ? 'private_pending_reviews_omitted' : page.reason,
      }
    },
    true,
  )
}

function isFinding(params: GitHubPRParams): params is GitHubFinding {
  return 'review_comment' in params
}
