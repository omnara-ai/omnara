import type { JsonBody } from '@omnara/sdk'
import { z } from 'zod'

import { GitHubAPIError, githubDatabaseID, githubNodeID } from './protocol'

export const nativeControlID = githubDatabaseID.refine(
  (value) => BigInt(value) <= BigInt(Number.MAX_SAFE_INTEGER),
)
export const githubInstallation = z.object({
  id: nativeControlID,
  app_id: nativeControlID,
  suspended_at: z.iso.datetime().nullable(),
  permissions: z.record(z.string(), z.string()),
})
export function installationCanCommunicate(value: z.infer<typeof githubInstallation>): boolean {
  return (
    value.suspended_at === null &&
    value.permissions.pull_requests === 'write' &&
    ['read', 'write'].includes(value.permissions.issues ?? '') &&
    value.permissions.metadata === 'read'
  )
}

export const controlRepositoryQuery = `query RepositoryControlIdentity($id: ID!) {
  node(id: $id) {
    __typename
    id
    ... on Repository { name owner { id login } }
  }
}`
export const controlRepository = z.object({
  __typename: z.literal('Repository'),
  id: githubNodeID,
  name: z.string().regex(/^[A-Za-z0-9_.-]{1,100}$/),
  owner: z.object({
    id: githubNodeID,
    login: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9-]{0,99}$/),
  }),
})
const envelope = z.object({
  data: z.object({ node: controlRepository.nullable() }),
  errors: z
    .array(z.object({ type: z.string(), path: z.array(z.union([z.string(), z.number()])) }))
    .optional(),
})

/** Deliberately asymmetric: only an explicit null with no errors or exact-path
 * NOT_FOUND is unavailable to this installation. A missing field, partial
 * failure or unfamiliar provider error never disables a core connection.
 */
export function repositoryObservation(raw: JsonBody | null, expectedID: string) {
  const parsed = envelope.safeParse(raw)
  if (!parsed.success)
    throw new GitHubAPIError('repository_observation_inconclusive', { retryable: true })
  const { data, errors } = parsed.data
  if (errors?.length) {
    if (
      data.node !== null ||
      !errors.every(
        (error) =>
          error.type === 'NOT_FOUND' && error.path.length === 1 && error.path[0] === 'node',
      )
    )
      throw new GitHubAPIError('repository_observation_inconclusive', { retryable: true })
  }
  if (data.node !== null && data.node.id !== expectedID)
    throw new GitHubAPIError('repository_scope_mismatch')
  return data.node
}
