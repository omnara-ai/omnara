import type { ChannelOpaqueObject } from '@omnara/sdk'

import type { ReceiptWorkflowRequest } from '../core-client'
import { type GitHubEvent, githubInputKey, githubPRRef } from './events'
import { GitHubAPIError } from './protocol'

export function githubWorkflowInput(
  event: GitHubEvent,
): Omit<ReceiptWorkflowRequest, 'route_id' | 'instance_key' | 'target' | 'grants'> {
  const author = event.comment?.user ?? event.review?.user ?? event.sender
  const lines = [
    `GitHub ${event.event}/${event.action}: ${event.repository.owner.login}/${event.repository.name}#${event.pull_request.number}`,
    `Title: ${event.pull_request.title}`,
    `Observed PR state: ${event.pull_request.state}${event.pull_request.draft === true ? ' (draft)' : ''}`,
  ]
  if (event.pull_request.head) lines.push(`Observed head: ${event.pull_request.head.sha}`)
  if (event.before && event.after)
    lines.push(`Commit transition: ${event.before} -> ${event.after}`)
  if (event.pull_request.body) lines.push(`PR description:\n${event.pull_request.body}`)
  if (event.comment) lines.push(`Comment by ${event.comment.user.login}:\n${event.comment.body}`)
  if (event.comment?.path)
    lines.push(
      `Review location: ${JSON.stringify({
        path: event.comment.path,
        commit_id: event.comment.commit_id,
        original_commit_id: event.comment.original_commit_id,
        line: event.comment.line,
        side: event.comment.side,
        start_line: event.comment.start_line,
        start_side: event.comment.start_side,
        subject_type: event.comment.subject_type,
      })}`,
    )
  if (event.review)
    lines.push(
      `Review (${event.review.state}) by ${event.review.user.login}:\n${event.review.body ?? ''}`,
    )
  const metadata: ChannelOpaqueObject = {
    provider: 'github',
    event_type: event.event,
    action: event.action,
    delivery_id: event.delivery_id,
    raw_body_sha256: event.raw_body_sha256,
    repository_id: event.repository.id,
    pr_number: event.pull_request.number,
    provider_ref: githubPRRef(event),
  }
  if (event.comment) {
    metadata.comment_id = event.comment.id
    metadata.comment_node_id = event.comment.node_id
  }
  if (event.review) {
    metadata.review_id = event.review.id
    metadata.review_node_id = event.review.node_id
  }
  const input = {
    input_key: githubInputKey(event),
    author: { ref: author.id, display_name: author.login },
    content_blocks: [{ type: 'text' as const, text: lines.join('\n\n') }],
    metadata,
    delivery_mode: 'steering' as const,
    cancel_open_interactions: true,
  }
  if (Buffer.byteLength(JSON.stringify(input)) > 1024 * 1024)
    throw new GitHubAPIError('input_too_large')
  return input
}
