import type { ChannelOperationDestination, ChannelReplyDestination } from '@omnara/sdk'

import type { GitHubClient } from './client'
import { GitHubAPIError, githubNodeID, githubPRNumber } from './protocol'

export interface GitHubAddress {
  number: number
  threadID?: string
  rootID?: string
}

export function githubAddress(
  client: GitHubClient,
  destination: ChannelOperationDestination,
): GitHubAddress {
  const match = /^repo:([1-9][0-9]*):pr:([1-9][0-9]*)(?::comment:([A-Za-z0-9_+=/-]+))?$/.exec(
    destination.provider_ref,
  )
  if (!match || match[1] !== String(client.configuration.repositoryID))
    throw new GitHubAPIError('invalid_address')
  const number = githubPRNumber.safeParse(Number(match[2]))
  if (!number.success) throw new GitHubAPIError('invalid_address')
  const rootID = match[3]
  if (!rootID) {
    if (destination.implementation_key !== 'github_pr' || destination.provider_ref_kind !== 'pr')
      throw new GitHubAPIError('unsupported_address')
    return { number: number.data }
  }
  const thread = githubNodeID.safeParse(destination.provider_metadata.thread_id)
  if (
    destination.implementation_key !== 'github_review_thread' ||
    destination.provider_ref_kind !== 'review_thread' ||
    !thread.success ||
    !githubNodeID.safeParse(rootID).success
  )
    throw new GitHubAPIError('invalid_address')
  return { number: number.data, rootID, threadID: thread.data }
}

export function githubReplyDestination(
  client: GitHubClient,
  number: number,
  rootID: string,
  threadID: string,
): ChannelReplyDestination {
  return {
    implementation_key: 'github_review_thread',
    provider_ref: `repo:${client.configuration.repositoryID}:pr:${number}:comment:${rootID}`,
    provider_ref_kind: 'review_thread',
    provider_metadata: { thread_id: threadID },
  }
}
