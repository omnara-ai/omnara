import { createHash } from 'node:crypto'

import type { JsonBody } from '@omnara/sdk'
import { z } from 'zod'

import { parseObjectFields } from '../json'
import { githubCommitID, githubDatabaseID, githubNodeID, githubPRNumber } from './protocol'

export const githubWebhookBytes = 2 * 1024 * 1024
const text = z.string().refine((value) => !value.includes('\u0000'))
const bodyText = text.refine((value) => Buffer.byteLength(value) <= 64 * 1024)
const runtimeID = githubDatabaseID.refine(
  (value) => BigInt(value) <= BigInt(Number.MAX_SAFE_INTEGER),
)
const author = z.object({
  id: githubDatabaseID,
  node_id: githubNodeID,
  login: text.min(1).max(256),
  type: text.max(32),
})
const repository = z.object({
  id: runtimeID,
  node_id: githubNodeID,
  name: text.regex(/^[A-Za-z0-9_.-]{1,100}$/),
  owner: z.object({ login: text.regex(/^[A-Za-z0-9][A-Za-z0-9-]{0,99}$/) }),
})
const pullRequest = z.object({
  id: githubDatabaseID,
  node_id: githubNodeID,
  number: githubPRNumber,
  title: bodyText,
  body: bodyText.nullable(),
  state: z.enum(['open', 'closed']),
  updated_at: z.iso.datetime(),
  draft: z.boolean().optional(),
  head: z.object({ sha: githubCommitID }).optional(),
  base: z.object({ sha: githubCommitID }).optional(),
})
const comment = z.object({
  id: githubDatabaseID,
  node_id: githubNodeID,
  user: author,
  body: bodyText,
  created_at: z.iso.datetime(),
  updated_at: z.iso.datetime(),
  in_reply_to_id: githubDatabaseID.optional(),
  path: text.max(1024).optional(),
  line: githubPRNumber.nullable().optional(),
  original_line: githubPRNumber.nullable().optional(),
  start_line: githubPRNumber.nullable().optional(),
  original_start_line: githubPRNumber.nullable().optional(),
  side: z.enum(['LEFT', 'RIGHT']).optional(),
  start_side: z.enum(['LEFT', 'RIGHT']).nullable().optional(),
  subject_type: z.enum(['line', 'file']).optional(),
  commit_id: githubCommitID.optional(),
  original_commit_id: githubCommitID.optional(),
})
const review = z.object({
  id: githubDatabaseID,
  node_id: githubNodeID,
  user: author,
  body: bodyText.nullable(),
  state: z.enum(['commented', 'approved', 'changes_requested', 'dismissed']),
  commit_id: githubCommitID,
  submitted_at: z.iso.datetime(),
})

export const githubEvent = z.strictObject({
  delivery_id: z.uuid(),
  raw_body_sha256: z.string().regex(/^[0-9a-f]{64}$/),
  event: z.enum([
    'pull_request',
    'issue_comment',
    'pull_request_review',
    'pull_request_review_comment',
  ]),
  action: text.min(1).max(64),
  installation_id: runtimeID,
  repository,
  pull_request: pullRequest,
  sender: author,
  comment: comment.optional(),
  review: review.optional(),
  before: githubCommitID.optional(),
  after: githubCommitID.optional(),
  // PR edited callbacks also cover title/base changes. Retain only whether the
  // description changed; old descriptions are unnecessary for activation.
  body_edited: z.boolean().optional(),
})
export type GitHubEvent = z.infer<typeof githubEvent>
export const githubLifecycleEvent = z.strictObject({
  delivery_id: z.uuid(),
  raw_body_sha256: z.string().regex(/^[0-9a-f]{64}$/),
  event: z.enum(['installation', 'installation_repositories']),
  action: text.min(1).max(64),
  installationID: runtimeID,
  appID: runtimeID,
})
export type GitHubLifecycleEvent = z.infer<typeof githubLifecycleEvent>
export type GitHubWebhookEvent = GitHubEvent | GitHubLifecycleEvent | undefined

/** Called only after raw-byte signature verification. Existing strict parsing
 * rejects duplicate keys/depth; selected native numbers are read from original
 * field slices before JSON.parse could round them. No generic JSON walker.
 */
export function projectGitHubEvent(
  raw: string,
  event: string,
  deliveryID: string,
): GitHubWebhookEvent {
  const fields = parseObjectFields(raw, githubWebhookBytes)
  if (event === 'ping') return undefined
  const action = z.string().parse(JSON.parse(fields.get('action') ?? 'null'))
  if (event === 'installation' || event === 'installation_repositories') {
    const install = nativeObject(required(fields, 'installation'), ['id', 'app_id'])
    return githubLifecycleEvent.parse({
      delivery_id: deliveryID,
      raw_body_sha256: createHash('sha256').update(raw).digest('hex'),
      event,
      action,
      installationID: runtimeID.parse(install.id),
      appID: runtimeID.parse(install.app_id),
    })
  }
  const actions = {
    pull_request: [
      'opened',
      'synchronize',
      'reopened',
      'closed',
      'edited',
      'ready_for_review',
      'converted_to_draft',
    ],
    issue_comment: ['created', 'edited', 'deleted'],
    pull_request_review: ['submitted', 'edited', 'dismissed'],
    pull_request_review_comment: ['created', 'edited', 'deleted'],
  }
  const eventType = githubEvent.shape.event.safeParse(event)
  if (!eventType.success || !actions[eventType.data].includes(action)) return undefined
  const installation = nativeObject(required(fields, 'installation'), ['id'])
  const repo = nativeObject(required(fields, 'repository'), ['id'])
  let pr = nativeObject(required(fields, event === 'issue_comment' ? 'issue' : 'pull_request'), [
    'id',
  ])
  if (event === 'issue_comment' && !pr.pull_request) return undefined
  pr = pullRequest.parse(pr)
  const result: GitHubEvent = githubEvent.parse({
    delivery_id: deliveryID,
    raw_body_sha256: createHash('sha256').update(raw).digest('hex'),
    event,
    action,
    installation_id: installation.id,
    repository: repo,
    pull_request: pr,
    sender: nativeObject(required(fields, 'sender'), ['id']),
    comment: fields.has('comment') ? projectedComment(required(fields, 'comment')) : undefined,
    review: fields.has('review') ? projectedReview(required(fields, 'review')) : undefined,
    before: fields.has('before')
      ? githubCommitID.parse(JSON.parse(required(fields, 'before')))
      : undefined,
    after: fields.has('after')
      ? githubCommitID.parse(JSON.parse(required(fields, 'after')))
      : undefined,
    body_edited:
      event === 'pull_request' && action === 'edited'
        ? z
            .object({ body: z.object({}).optional() })
            .parse(JSON.parse(fields.get('changes') ?? '{}')).body !== undefined
        : undefined,
  })
  if (event === 'pull_request' && action === 'synchronize' && (!result.before || !result.after))
    throw new Error('invalid GitHub commit event')
  if ((event === 'issue_comment' || event === 'pull_request_review_comment') && !result.comment)
    throw new Error('missing GitHub comment')
  if (event === 'pull_request_review' && !result.review) throw new Error('missing GitHub review')
  // Bound the saved projection and the eventual serialized input before ack.
  if (Buffer.byteLength(JSON.stringify(result)) > 1024 * 1024)
    throw new Error('GitHub normalized input too large')
  return result
}

function projectedComment(raw: string) {
  const fields = parseObjectFields(raw, githubWebhookBytes)
  return comment.parse({
    ...nativeObject(raw, ['id', 'in_reply_to_id']),
    user: nativeObject(required(fields, 'user'), ['id']),
  })
}

function projectedReview(raw: string) {
  const fields = parseObjectFields(raw, githubWebhookBytes)
  return review.parse({
    ...nativeObject(raw, ['id']),
    user: nativeObject(required(fields, 'user'), ['id']),
  })
}

function nativeObject(raw: string, ids: readonly string[]): Record<string, JsonBody> {
  const fields = parseObjectFields(raw, githubWebhookBytes)
  const result = z.record(z.string(), z.json()).parse(JSON.parse(raw))
  for (const name of ids) {
    const value = fields.get(name)
    if (value !== undefined) result[name] = githubDatabaseID.parse(value)
  }
  return result
}

function required(fields: ReadonlyMap<string, string>, name: string): string {
  const value = fields.get(name)
  if (value === undefined) throw new Error('incomplete GitHub event')
  return value
}

export function githubPRRef(event: GitHubEvent): string {
  return `repo:${event.repository.id}:pr:${event.pull_request.number}`
}

export function githubInputKey(event: GitHubEvent): string {
  const prefix = `github:${githubPRRef(event)}`
  if (event.comment) {
    const edited =
      event.action === 'edited'
        ? `:${event.comment.updated_at}:${bodyDigest(event.comment.body)}`
        : ''
    return `${prefix}:${event.event}:${event.comment.id}:${event.action}${edited}`
  }
  if (event.review) {
    const edited = event.action === 'edited' ? `:${bodyDigest(event.review.body ?? '')}` : ''
    return `${prefix}:review:${event.review.id}:${event.action}${edited}`
  }
  if (event.action === 'synchronize') return `${prefix}:commit:${event.before}:${event.after}`
  if (event.action === 'opened') return `${prefix}:opened`
  return `${prefix}:${event.action}:${event.pull_request.updated_at}:${bodyDigest(event.pull_request.body ?? '')}`
}

function bodyDigest(body: string): string {
  return createHash('sha256').update(body).digest('hex')
}
