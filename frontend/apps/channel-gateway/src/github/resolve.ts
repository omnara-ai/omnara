import type { ChannelRegistrationTarget, ChannelResolveAddressOperation } from '@omnara/sdk'

import type { CoreClient } from '../core-client'
import { type OperationRetryOptions, retryOperation } from '../operation-retry'
import { githubDefinition } from './behavior'
import type { GitHubClient } from './client'
import { githubInboundThread } from './inbound-thread'
import { getGitHubPullRequest } from './messages'
import { GitHubAPIError, githubPRNumber } from './protocol'

export class GitHubAddressError extends Error {
  constructor(readonly code: 'invalid_address' | 'unsupported_address' | 'address_unavailable') {
    super(`GitHub address resolution failed: ${code}`)
  }
}

/** Setup has no agent scope: only published threads can be registered here.
 * Native reads prove repository and parentage; core registers the inline parent
 * atomically with the child and grants no access merely from that parentage.
 */
export async function resolveGitHubAddress(
  client: GitHubClient,
  input: ChannelResolveAddressOperation,
  context: {
    installation: Parameters<CoreClient['publishDefinition']>[0]
    publishDefinition: CoreClient['publishDefinition']
  },
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<ChannelRegistrationTarget> {
  const match = /^repo:([1-9][0-9]*):pr:([1-9][0-9]*)(?::comment:([A-Za-z0-9_+=/-]{1,512}))?$/.exec(
    input.provider_ref,
  )
  if (
    !match ||
    match[1] !== String(client.configuration.repositoryID) ||
    !githubPRNumber.safeParse(Number(match[2])).success
  )
    throw new GitHubAddressError('invalid_address')
  const number = Number(match[2])
  const commentID = match[3]
  const kind = commentID ? 'review_thread' : 'pr'
  if (
    input.provider_ref_kind !== undefined &&
    !['pr', 'review_thread'].includes(input.provider_ref_kind)
  )
    throw new GitHubAddressError('unsupported_address')
  if (input.provider_ref_kind !== undefined && input.provider_ref_kind !== kind)
    throw new GitHubAddressError('invalid_address')
  const result = await retryOperation({ ...operation, idempotent: true }, async (attempt) => {
    let thread
    try {
      await getGitHubPullRequest(client, number, attempt)
      if (commentID) {
        thread = await githubInboundThread(client, number, commentID, attempt)
        // A reply locator must not silently become a different registered root.
        if (!thread || thread.rootID !== commentID)
          return new GitHubAddressError('address_unavailable')
      }
    } catch (cause) {
      if (cause instanceof GitHubAPIError && !cause.retryable)
        return new GitHubAddressError('address_unavailable')
      throw cause
    }
    const parentDefinition = await context.publishDefinition(
      context.installation,
      githubDefinition('pr'),
      attempt.signal,
    )
    const parent = {
      definition_id: parentDefinition.id,
      provider_ref: `repo:${client.configuration.repositoryID}:pr:${number}`,
      provider_ref_kind: 'pr',
      display_name: `${client.configuration.repositoryOwner}/${client.configuration.repositoryName}#${number}`,
    }
    if (!thread) return parent
    const definition = await context.publishDefinition(
      context.installation,
      githubDefinition('review_thread'),
      attempt.signal,
    )
    return {
      definition_id: definition.id,
      provider_ref: input.provider_ref,
      provider_ref_kind: kind,
      provider_metadata: { thread_id: thread.threadID },
      display_name: `${parent.display_name} review thread`,
      parent,
    }
  })
  if (result instanceof GitHubAddressError) throw result
  return result
}
