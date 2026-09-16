import { createAppAuth } from '@octokit/auth-app'
import type { JsonBody } from '@omnara/sdk'
import { z } from 'zod'

import { parseObjectFields } from '../json'
import type { OperationAttemptContext } from '../operations/retry'
import type { GitHubConfiguration } from './configuration'
import { type GitHubDocument, githubDocuments } from './documents'
import {
  GitHubAPIError,
  githubDatabaseID,
  githubFileFinding,
  type GitHubFinding,
  githubLineFinding,
  githubNodeID,
  githubPRNumber,
  type GitHubVariables,
  providerValue,
  validateGitHubText,
} from './protocol'
import { githubGraphQLRateLimit, githubRateLimitDelay } from './rate-limit'

const responseBytes = 1024 * 1024
const repositorySchema = z.object({
  id: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
  node_id: githubNodeID,
  name: z.string().min(1).max(100),
  owner: z.object({ login: z.string().min(1).max(100) }),
})
const tokenSchema = z.object({
  token: z.string().min(1).max(4096),
  expires_at: z.iso.datetime(),
})
const graphqlEnvelope = z.object({
  data: z.json().optional(),
  errors: z
    .array(z.object({ type: z.string().optional(), message: z.string().optional() }))
    .optional(),
})

// REST comment responses also contain numeric database IDs that may exceed JS
// precision. These writes consume only the opaque node ID; readback verifies its
// publication and scope. Project before numeric decoding, without rounding IDs.
function commentIdentityJSON(fields: ReadonlyMap<string, string>): string {
  return `{"node_id":${fields.get('node_id') ?? 'null'}}`
}

/** Shared only within one app/install revision. Mutable repository observations
 * and request signals belong to each client, never to this runtime auth memo.
 */
export interface GitHubAuthentication {
  token?: { value: string; expiresAt: number }
  viewer?: { token: NonNullable<GitHubAuthentication['token']>; id: string }
}

/** One project installation/repository. apiUrl is operator/test configuration only.
 * auth-app signs JWTs; its request hook is deliberately not installed because it
 * retries 401s internally. Native fetch has no retry/throttling hooks.
 */
export class GitHubClient {
  readonly configuration: Readonly<GitHubConfiguration>
  private readonly base: URL
  private readonly appAuth: ReturnType<typeof createAppAuth>
  private repositoryAddress?: { owner: string; name: string }

  constructor(
    configuration: GitHubConfiguration,
    apiUrl = 'https://api.github.com',
    private readonly authentication: GitHubAuthentication = {},
  ) {
    this.configuration = { ...configuration }
    try {
      this.base = new URL(apiUrl.replace(/\/$/, ''))
      const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(this.base.hostname)
      if (
        this.base.username ||
        this.base.password ||
        this.base.search ||
        this.base.hash ||
        (this.base.protocol !== 'https:' && !(loopback && this.base.protocol === 'http:')) ||
        !Number.isSafeInteger(configuration.installationID) ||
        configuration.installationID <= 0 ||
        !Number.isSafeInteger(configuration.repositoryID) ||
        configuration.repositoryID <= 0 ||
        !githubNodeID.safeParse(configuration.repositoryNodeID).success
      )
        throw new Error('invalid configuration')
      this.appAuth = createAppAuth({
        appId: configuration.appID,
        privateKey: configuration.privateKey,
      })
    } catch {
      throw new GitHubAPIError('invalid_configuration')
    }
  }

  /** The authenticated viewer is verified, never derived from an app slug.
   * Refresh with the token; every new receipt client still verifies its repo.
   */
  async viewerID(context: OperationAttemptContext): Promise<string> {
    await this.installationToken(context)
    const token = this.authentication.token
    if (token && this.authentication.viewer?.token === token) return this.authentication.viewer.id
    const result = await this.query(
      'viewer',
      {},
      z.object({ viewer: z.object({ id: githubNodeID }) }),
      context,
    )
    if (token && this.authentication.token === token)
      this.authentication.viewer = { token, id: result.viewer.id }
    return result.viewer.id
  }

  /** Fixed operation documents; callers cannot supply URLs or arbitrary GraphQL. */
  async query<T>(
    document: GitHubDocument,
    variables: GitHubVariables,
    schema: z.ZodType<T>,
    context: OperationAttemptContext,
  ): Promise<T> {
    const query = githubDocuments[document]
    const mutation = query.startsWith('mutation ')
    const token = await this.installationToken(context)
    const result = await this.request(
      'POST',
      '/graphql',
      token,
      JSON.stringify({ query, variables }),
      context,
      mutation,
    )
    const envelope = providerValue(graphqlEnvelope, result.body, mutation)
    if (envelope.errors?.length) {
      const types = envelope.errors.map((error) => error.type)
      const rateLimit = githubGraphQLRateLimit(envelope.errors, result.headers)
      if (rateLimit)
        throw new GitHubAPIError('rate_limited', {
          retryable: !mutation,
          outcomeUnknown: mutation,
          retryAfterMs: githubRateLimitDelay(result.headers, Date.now(), rateLimit === 'primary'),
        })
      const emptyData =
        envelope.data == null || z.record(z.string(), z.null()).safeParse(envelope.data).success
      const definite =
        emptyData &&
        types.every(
          (type) => type === 'NOT_FOUND' || type === 'FORBIDDEN' || type === 'UNPROCESSABLE',
        )
      throw new GitHubAPIError(
        types.every((type) => type === 'NOT_FOUND') ? 'resource_unavailable' : 'provider_rejected',
        {
          outcomeUnknown: mutation && !definite,
        },
      )
    }
    return providerValue(schema, envelope.data, mutation)
  }

  /** Dedicated REST reply: never fall back to an implicit GraphQL pending review.
   * The root comment ID stays decimal text through the URL, including >2^53 IDs.
   */
  async publishedReply(
    number: number,
    rootID: string,
    text: string,
    context: OperationAttemptContext,
  ): Promise<string> {
    providerValue(githubPRNumber, number)
    providerValue(githubDatabaseID, rootID)
    validateGitHubText(text)
    const token = await this.installationToken(context)
    // Resolve by immutable ID again: a rename can happen after earlier queries.
    const repository = await this.verifyRepository(token, context)
    const path = `/repos/${encodeURIComponent(repository.owner)}/${encodeURIComponent(repository.name)}/pulls/${number}/comments/${rootID}/replies`
    const result = await this.request(
      'POST',
      path,
      token,
      JSON.stringify({ body: text }),
      context,
      true,
      commentIdentityJSON,
    )
    if (result.status !== 201)
      throw new GitHubAPIError('invalid_response', { outcomeUnknown: true })
    return providerValue(z.object({ node_id: githubNodeID }), result.body, true).node_id
  }

  /** One immediate inline/file comment. No pending review ID or publication step.
   * https://docs.github.com/en/rest/pulls/comments#create-a-review-comment-for-a-pull-request
   */
  async publishedComment(
    number: number,
    text: string,
    params: GitHubFinding,
    context: OperationAttemptContext,
  ): Promise<string> {
    providerValue(githubPRNumber, number)
    providerValue(z.union([githubLineFinding, githubFileFinding]), params)
    validateGitHubText(text)
    const token = await this.installationToken(context)
    const repository = await this.verifyRepository(token, context)
    const path = `/repos/${encodeURIComponent(repository.owner)}/${encodeURIComponent(repository.name)}/pulls/${number}/comments`
    const result = await this.request(
      'POST',
      path,
      token,
      JSON.stringify({ body: text, ...params, commit_id: params.commit_id.toLowerCase() }),
      context,
      true,
      commentIdentityJSON,
    )
    if (result.status !== 201)
      throw new GitHubAPIError('invalid_response', { outcomeUnknown: true })
    return providerValue(z.object({ node_id: githubNodeID }), result.body, true).node_id
  }

  private async installationToken(context: OperationAttemptContext): Promise<string> {
    const cached = this.authentication.token
    if (cached && cached.expiresAt - Date.now() > 60_000) {
      if (!this.repositoryAddress) await this.verifyRepository(cached.value, context)
      return cached.value
    }
    let jwt: string
    try {
      jwt = (await this.appAuth({ type: 'app' })).token
    } catch {
      throw new GitHubAPIError('invalid_configuration')
    }
    context.signal.throwIfAborted()
    const result = await this.request(
      'POST',
      `/app/installations/${this.configuration.installationID}/access_tokens`,
      jwt,
      JSON.stringify({
        repository_ids: [this.configuration.repositoryID],
        permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
      }),
      context,
      false,
    )
    const token = providerValue(tokenSchema, result.body)
    const expiresAt = Date.parse(token.expires_at)
    if (expiresAt - Date.now() <= 60_000) throw new GitHubAPIError('invalid_response')
    await this.verifyRepository(token.token, context)
    this.authentication.token = { value: token.token, expiresAt }
    this.authentication.viewer = undefined
    return token.token
  }

  private async verifyRepository(token: string, context: OperationAttemptContext) {
    // Do not trust a caller's owner/name or an installation token's broad default.
    // Numeric identity and node identity must both match the project connection.
    const repository = providerValue(
      repositorySchema,
      (
        await this.request(
          'GET',
          `/repositories/${this.configuration.repositoryID}`,
          token,
          undefined,
          context,
          false,
        )
      ).body,
    )
    if (
      repository.id !== this.configuration.repositoryID ||
      repository.node_id !== this.configuration.repositoryNodeID
    )
      throw new GitHubAPIError('repository_scope_mismatch')
    this.repositoryAddress = { owner: repository.owner.login, name: repository.name }
    return this.repositoryAddress
  }

  private async request(
    method: 'GET' | 'POST',
    path: string,
    token: string,
    data: string | undefined,
    context: OperationAttemptContext,
    mutation: boolean,
    projectResponse?: (fields: ReadonlyMap<string, string>) => string,
  ): Promise<{ body: JsonBody; headers: Headers; status: number }> {
    context.signal.throwIfAborted()
    if (Date.now() >= context.deadlineMs) throw new GitHubAPIError('deadline_exceeded')
    if (data !== undefined && Buffer.byteLength(data) > 256 * 1024)
      throw new GitHubAPIError('request_too_large')
    const signal = AbortSignal.any([
      context.signal,
      AbortSignal.timeout(Math.min(2_147_483_647, Math.max(1, context.deadlineMs - Date.now()))),
    ])
    let response: Response | undefined
    try {
      const request: RequestInit = {
        method,
        headers: {
          authorization: `Bearer ${token}`,
          accept: 'application/vnd.github.v3+json',
          'content-type': 'application/json',
          'user-agent': 'omnara-channel-gateway',
          // Preserve the communication API default; app-client.ts pins 2026 for control reads.
          'x-github-api-version': '2022-11-28',
        },
        redirect: 'manual',
        signal,
      }
      if (method === 'POST') request.body = data
      response = await fetch(`${this.base.href.replace(/\/$/, '')}${path}`, request)
      if (!response.ok) {
        // Drop only the rejected cached token, preserving any concurrent refresh.
        // Safe reads retry through the caller's budget, never inside this client.
        if (response.status === 401 && this.authentication.token?.value === token) {
          this.authentication.token = undefined
          this.authentication.viewer = undefined
        }
        if (
          response.status === 429 ||
          (response.status === 403 &&
            (response.headers.has('retry-after') ||
              response.headers.get('x-ratelimit-remaining') === '0'))
        )
          throw new GitHubAPIError('rate_limited', {
            retryable: true,
            retryAfterMs: githubRateLimitDelay(response.headers),
          })
        // Secondary limits can return 403 with remaining primary quota and
        // no retry header. Retry safe reads conservatively; an unclassified
        // rejection never authorizes replaying a native content mutation.
        if (response.status === 403 && !mutation)
          throw new GitHubAPIError('http_rejected', {
            retryable: true,
            retryAfterMs: githubRateLimitDelay(response.headers),
          })
        const ambiguous = response.status >= 500 || response.status === 408
        throw new GitHubAPIError(
          response.status === 404 ? 'resource_unavailable' : 'http_rejected',
          {
            outcomeUnknown: mutation && ambiguous,
            retryable: !mutation && (ambiguous || response.status === 401),
          },
        )
      }
      const reader = response.body?.getReader()
      if (!reader) throw new GitHubAPIError('invalid_response', { outcomeUnknown: mutation })
      const chunks: Uint8Array[] = []
      let bytes = 0
      try {
        for (;;) {
          const chunk = await reader.read()
          if (chunk.done) break
          bytes += chunk.value.byteLength
          if (bytes > responseBytes)
            throw new GitHubAPIError('response_too_large', { outcomeUnknown: mutation })
          chunks.push(chunk.value)
        }
      } finally {
        await reader.cancel().catch(() => undefined)
      }
      const raw = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks, bytes))
      const fields = parseObjectFields(raw, responseBytes)
      if (
        path === `/repositories/${this.configuration.repositoryID}` &&
        fields.get('id') !== String(this.configuration.repositoryID)
      ) {
        throw new GitHubAPIError('repository_scope_mismatch')
      }
      const decoded: unknown = JSON.parse(
        projectResponse ? projectResponse(fields) : raw,
        (_key: string, value: JsonBody, source?: { source?: string }) => {
          // Never silently round integer fields in the consumed response.
          if (source?.source && /^-?\d+$/.test(source.source) && !Number.isSafeInteger(value))
            throw new GitHubAPIError('invalid_response', { outcomeUnknown: mutation })
          return value
        },
      )
      const parsed = z.json().safeParse(decoded)
      if (!parsed.success)
        throw new GitHubAPIError('invalid_response', { outcomeUnknown: mutation })
      return { body: parsed.data, headers: response.headers, status: response.status }
    } catch (error) {
      if (error instanceof GitHubAPIError) throw error
      throw new GitHubAPIError('provider_unavailable', {
        outcomeUnknown: mutation,
        retryable: !mutation,
      })
    } finally {
      if (response && !response.bodyUsed) await response.body?.cancel().catch(() => undefined)
    }
  }
}
