import type { JsonBody } from '@omnara/sdk'
import { z } from 'zod'

import { parseObjectFields } from '../operations-json'
import { ProviderDeliveryError } from '../types'

export class GitHubAPIError extends ProviderDeliveryError {
  constructor(
    readonly code: string,
    options: { retryable?: boolean; outcomeUnknown?: boolean; retryAfterMs?: number } = {},
  ) {
    super(`GitHub operation failed: ${code}`, options)
    this.name = 'GitHubAPIError'
  }
}

export const githubNodeID = z
  .string()
  .min(1)
  .max(512)
  .regex(/^[A-Za-z0-9_+=/-]+$/)
export const githubCommitID = z.string().regex(/^[0-9a-fA-F]{40}$/)
export const githubPRNumber = z.number().int().min(1).max(2_147_483_647)
// GitHub's BigInt scalar is a decimal string, not a JavaScript number.
export const githubDatabaseID = z
  .string()
  .regex(/^[1-9][0-9]{0,18}$/)
  .refine((value) => BigInt(value) <= 9_223_372_036_854_775_807n)
const line = githubPRNumber
const side = z.enum(['LEFT', 'RIGHT'])
const path = z
  .string()
  .min(1)
  .refine((text) => Buffer.byteLength(text) <= 1024 && !text.includes('\u0000'))
const findingFields = {
  commit_id: githubCommitID,
  path,
}
export const githubLineFinding = z
  .strictObject({
    ...findingFields,
    subject_type: z.literal('line').optional(),
    line,
    side,
    start_line: line.optional(),
    start_side: side.optional(),
  })
  .refine((value) => (value.start_side === undefined) === (value.start_line === undefined))
export const githubFileFinding = z.strictObject({
  ...findingFields,
  subject_type: z.literal('file'),
})
export const githubPRParams = z.union([z.strictObject({}), githubLineFinding, githubFileFinding])
export type GitHubFinding = z.infer<typeof githubLineFinding> | z.infer<typeof githubFileFinding>
export type GitHubPRParams = z.infer<typeof githubPRParams>

export interface GitHubVariables {
  repository?: string
  number?: number
  thread?: string
  comment?: string
  limit?: number
  before?: string
  after?: string
  author?: string
  input?: { subjectId: string; body: string }
}

/** The definition and transport validate the same fixed shape; no coercion or defaults. */
export function parseGitHubParams(raw = '{}', kind: 'pr' | 'review_thread' = 'pr'): GitHubPRParams {
  try {
    parseObjectFields(raw, 16 * 1024)
    const parsed = (kind === 'pr' ? githubPRParams : z.strictObject({})).safeParse(JSON.parse(raw))
    if (parsed.success) return parsed.data
  } catch {
    /* Only fixed diagnostics cross the provider boundary. */
  }
  throw new GitHubAPIError('invalid_params')
}

export const githubSendParamsSchema = {
  ...z.toJSONSchema(githubPRParams, { unrepresentable: 'any' }),
  dependentRequired: { start_side: ['start_line'], start_line: ['start_side'] },
}
export const githubThreadParamsSchema = z.toJSONSchema(z.strictObject({}))
export const githubAuthor = z.object({ login: z.string().min(1).max(256) }).nullable()
export const githubReview = z.object({
  id: githubNodeID,
  body: z.string(),
  state: z.enum(['PENDING', 'COMMENTED', 'APPROVED', 'CHANGES_REQUESTED', 'DISMISSED']),
  commit: z.object({ oid: githubCommitID }).nullable(),
  author: githubAuthor,
  createdAt: z.iso.datetime(),
  submittedAt: z.iso.datetime().nullable(),
})
export const githubComment = z.object({
  id: githubNodeID,
  body: z.string(),
  author: githubAuthor,
  createdAt: z.iso.datetime(),
})
export const githubReviewComment = githubComment.extend({
  fullDatabaseId: githubDatabaseID.nullable(),
  state: z.enum(['PENDING', 'SUBMITTED']),
  path: z.string(),
  line: line.nullable(),
  originalCommit: z.object({ oid: githubCommitID }).nullable(),
  pullRequestReview: z.object({ id: githubNodeID, state: githubReview.shape.state }).nullable(),
  replyTo: z.object({ id: githubNodeID }).nullable(),
})
export const githubPRIdentity = z.object({
  id: githubNodeID,
  number: githubPRNumber,
  repository: z.object({ id: githubNodeID }),
})
export const githubPageInfo = z.object({
  hasPreviousPage: z.boolean(),
  startCursor: z.string().min(1).max(2048).nullable(),
})
export type GitHubReview = z.infer<typeof githubReview>
export type GitHubComment = z.infer<typeof githubComment>
export type GitHubReviewComment = z.infer<typeof githubReviewComment>

export function validateGitHubText(text: string): void {
  if (
    !text.trim() ||
    Buffer.byteLength(text) > 64 * 1024 ||
    Buffer.from(text).toString('utf8') !== text ||
    text.includes('\u0000')
  )
    throw new GitHubAPIError('invalid_message')
}

export function providerValue<T>(
  schema: z.ZodType<T>,
  value: JsonBody | undefined,
  mutation = false,
): T {
  const parsed = schema.safeParse(value)
  if (!parsed.success) throw new GitHubAPIError('invalid_response', { outcomeUnknown: mutation })
  return parsed.data
}
