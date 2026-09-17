import { createGitHubAdapter, type GitHubAdapter } from '@chat-adapter/github'
import { createAppAuth } from '@octokit/auth-app'
import { ConsoleLogger } from 'chat'
import { z } from 'zod'

import { ProviderResponseTooLargeError, readProviderResponseBody } from '../http-io'
import { parseObjectFields } from '../json'
import { currentProviderOperation, withProviderOperation } from '../operations/provider-context'
import type { OperationAttemptContext } from '../operations/retry'
import type { GitHubConfiguration } from './configuration'
import { type GitHubDocument, githubDocuments } from './documents'
import {
  GitHubAPIError,
  type GitHubComment,
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

const commentIdentity = z.object({ node_id: githubNodeID })
const timelineResponse = commentIdentity.extend({
  body: z.string(),
  created_at: z.iso.datetime(),
  user: z.object({ login: z.string().min(1).max(256) }).nullable(),
})

/** Shared only within one app/install revision. Mutable repository observations
 * and request signals belong to each client, never to this runtime auth memo.
 */
export interface GitHubAuthentication {
  token?: { value: string; expiresAt: number }
  viewer?: { token: NonNullable<GitHubAuthentication['token']>; id: string; login: string }
}

/** One project installation/repository. apiUrl is operator/test configuration only.
 * auth-app only signs JWTs. The bare SDK adapter resolves our repository-narrowed
 * tokens; its Octokit request hook shares bounded, lossless transport with native
 * extensions. No Chat host, SDK webhook lifecycle, or hidden auth/retry loop.
 */
export class GitHubClient {
  readonly configuration: Readonly<GitHubConfiguration>
  private readonly base: URL
  private readonly adapter: GitHubAdapter
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
      // Core owns sanitized operation diagnostics; SDK logging includes request
      // options and author facts and must not write to journey stdout.
      const logger = new ConsoleLogger('silent')
      this.adapter = createGitHubAdapter({
        logger,
        apiUrl: this.base.href.replace(/\/$/, ''),
        installationToken: () => this.installationToken(currentProviderOperation()),
        // Durable native capture owns verification. Fail closed if this outbound
        // adapter is accidentally used as a webhook entrypoint.
        webhookVerifier: () => {
          throw new GitHubAPIError('unsupported_webhook_entrypoint')
        },
      })
      this.adapter.octokit.log = logger
      this.adapter.octokit.hook.wrap('request', async (request, options) => {
        const context = currentProviderOperation()
        let failure: unknown
        options.request = {
          ...options.request,
          signal: context.signal,
          fetch: async (url: string, init: RequestInit) => {
            try {
              return await this.boundedFetch(
                url,
                init,
                context,
                init.method === 'POST' && url !== `${this.base.href.replace(/\/$/, '')}/graphql`,
              )
            } catch (cause) {
              // Octokit wraps fetch failures in RequestError (retaining secrets).
              // Restore only our classified error outside that wrapper.
              failure = cause
              throw cause
            }
          },
        }
        try {
          return await request(options)
        } catch (cause) {
          if (cause instanceof GitHubAPIError) throw cause
          if (failure instanceof GitHubAPIError) throw failure
          throw new GitHubAPIError('provider_unavailable', { outcomeUnknown: true })
        }
      })
    } catch {
      throw new GitHubAPIError('invalid_configuration')
    }
  }

  /** Verified native identity, refreshed with the narrowed token. Never infer a
   * bot login from an app slug; it is also used for the pending-review guard. */
  async viewer(context: OperationAttemptContext): Promise<{ id: string; login: string }> {
    await this.installationToken(context)
    const token = this.authentication.token
    const cached = this.authentication.viewer
    if (token && cached?.token === token) return { id: cached.id, login: cached.login }
    const { viewer } = await this.query(
      'viewer',
      {},
      z.object({ viewer: z.object({ id: githubNodeID, login: z.string().min(1).max(256) }) }),
      context,
    )
    if (token && this.authentication.token === token)
      this.authentication.viewer = { token, ...viewer }
    return viewer
  }

  async viewerLogin(context: OperationAttemptContext): Promise<string> {
    return (await this.viewer(context)).login
  }

  /** Fixed read documents; callers cannot supply URLs or arbitrary GraphQL. */
  async query<T>(
    document: GitHubDocument,
    variables: GitHubVariables,
    schema: z.ZodType<T>,
    context: OperationAttemptContext,
  ): Promise<T> {
    return withProviderOperation(context, async () => {
      const result = await this.adapter.octokit.request('POST /graphql', {
        query: githubDocuments[document],
        variables,
      })
      const parsed = graphqlEnvelope.safeParse(result.data)
      if (!parsed.success) throw new GitHubAPIError('invalid_response')
      const envelope = parsed.data
      if (envelope.errors?.length) {
        const types = envelope.errors.map((error) => error.type)
        const headers = new Headers()
        for (const [name, value] of Object.entries(result.headers))
          if (value !== undefined) headers.set(name, String(value))
        const rateLimit = githubGraphQLRateLimit(envelope.errors, headers)
        if (rateLimit)
          throw new GitHubAPIError('rate_limited', {
            retryable: true,
            retryAfterMs: githubRateLimitDelay(headers, Date.now(), rateLimit === 'primary'),
          })
        throw new GitHubAPIError(
          types.every((type) => type === 'NOT_FOUND')
            ? 'resource_unavailable'
            : 'provider_rejected',
        )
      }
      return providerValue(schema, envelope.data)
    })
  }

  /** The public Octokit escape preserves Markdown verbatim. SDK postMessage
   * rewrites emoji placeholders even in raw text, so every send uses Octokit. */
  async publishedTimeline(
    number: number,
    text: string,
    context: OperationAttemptContext,
  ): Promise<GitHubComment> {
    providerValue(githubPRNumber, number)
    validateGitHubText(text)
    return withProviderOperation(context, async () => {
      const repository = await this.writeRepository(context)
      const result = await this.adapter.octokit.issues.createComment({
        owner: repository.owner,
        repo: repository.name,
        issue_number: number,
        body: text,
      })
      const parsed = timelineResponse.safeParse(result.data)
      if (!parsed.success) throw new GitHubAPIError('invalid_response', { outcomeUnknown: true })
      return {
        id: parsed.data.node_id,
        body: parsed.data.body,
        createdAt: parsed.data.created_at,
        author: parsed.data.user,
      }
    })
  }

  private async writeRepository(context: OperationAttemptContext) {
    const token = await this.installationToken(context)
    // Resolve immutable identity again: rename may occur after previous reads.
    return this.verifyRepository(token, context)
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
    return withProviderOperation(context, async () => {
      const repository = await this.writeRepository(context)
      // SDK decodeThreadId uses parseInt; keep every root as decimal text in
      // Octokit's route substitution. Never derive a GraphQL thread from this ID.
      const result = await this.adapter.octokit.request({
        method: 'POST',
        url: '/repos/{owner}/{repo}/pulls/{pull_number}/comments/{comment_id}/replies',
        owner: repository.owner,
        repo: repository.name,
        pull_number: number,
        comment_id: rootID,
        body: text,
      })
      const parsed = commentIdentity.safeParse(result.data)
      if (!parsed.success) throw new GitHubAPIError('invalid_response', { outcomeUnknown: true })
      return parsed.data.node_id
    })
  }

  /** Immediate inline/file placement has no SDK equivalent. No pending review. */
  async publishedComment(
    number: number,
    text: string,
    params: GitHubFinding,
    context: OperationAttemptContext,
  ): Promise<string> {
    providerValue(githubPRNumber, number)
    providerValue(z.union([githubLineFinding, githubFileFinding]), params)
    validateGitHubText(text)
    return withProviderOperation(context, async () => {
      const repository = await this.writeRepository(context)
      const result = await this.adapter.octokit.pulls.createReviewComment({
        owner: repository.owner,
        repo: repository.name,
        pull_number: number,
        body: text,
        ...params,
        commit_id: params.commit_id.toLowerCase(),
      })
      const parsed = commentIdentity.safeParse(result.data)
      if (!parsed.success) throw new GitHubAPIError('invalid_response', { outcomeUnknown: true })
      return parsed.data.node_id
    })
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
    const response = await this.boundedFetch(
      `/app/installations/${this.configuration.installationID}/access_tokens`,
      {
        method: 'POST',
        headers: { authorization: `Bearer ${jwt}` },
        body: JSON.stringify({
          repository_ids: [this.configuration.repositoryID],
          permissions: { pull_requests: 'write', issues: 'read', metadata: 'read' },
        }),
      },
      context,
    )
    const parsed = tokenSchema.safeParse(await response.json())
    if (!parsed.success) throw new GitHubAPIError('invalid_response')
    const token = parsed.data
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
    const response = await this.boundedFetch(
      `/repositories/${this.configuration.repositoryID}`,
      { headers: { authorization: `Bearer ${token}` } },
      context,
    )
    const parsed = repositorySchema.safeParse(await response.json())
    if (!parsed.success) throw new GitHubAPIError('invalid_response')
    const repository = parsed.data
    if (
      repository.id !== this.configuration.repositoryID ||
      repository.node_id !== this.configuration.repositoryNodeID
    )
      throw new GitHubAPIError('repository_scope_mismatch')
    this.repositoryAddress = { owner: repository.owner.login, name: repository.name }
    return this.repositoryAddress
  }

  /** Shared by Octokit and bootstrap auth without invoking the token resolver.
   * Validate/cap the wire body, then leave its single decode to the caller. */
  private async boundedFetch(
    path: string,
    init: RequestInit,
    context: OperationAttemptContext,
    mutation = false,
  ): Promise<Response> {
    context.signal.throwIfAborted()
    if (Date.now() >= context.deadlineMs) throw new GitHubAPIError('deadline_exceeded')
    const base = this.base.href.replace(/\/$/, '')
    const url = path.startsWith('/') ? `${base}${path}` : path
    const method = init.method ?? 'GET'
    const body = z.string().optional().safeParse(init.body)
    if (!url.startsWith(`${base}/`) || !['GET', 'POST'].includes(method) || !body.success)
      throw new GitHubAPIError('invalid_request')
    if (body.data !== undefined && Buffer.byteLength(body.data) > 256 * 1024)
      throw new GitHubAPIError('request_too_large')
    const headers = new Headers(init.headers)
    const token = (headers.get('authorization') ?? '').replace(/^(?:token|Bearer) /, '')
    headers.set('authorization', `Bearer ${token}`)
    headers.set('accept', 'application/vnd.github.v3+json')
    headers.set('content-type', 'application/json')
    headers.set('user-agent', 'omnara-channel-gateway')
    // Communication API default; app-client.ts pins 2026 for control reads.
    headers.set('x-github-api-version', '2022-11-28')
    const signal = AbortSignal.any([
      context.signal,
      AbortSignal.timeout(Math.min(2_147_483_647, Math.max(1, context.deadlineMs - Date.now()))),
    ])
    let response: Response | undefined
    try {
      response = await fetch(url, { ...init, method, headers, redirect: 'manual', signal })
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
      if (mutation && response.status !== 201)
        throw new GitHubAPIError('invalid_response', { outcomeUnknown: true })
      if (!response.body) throw new GitHubAPIError('invalid_response', { outcomeUnknown: mutation })
      const buffered = await readProviderResponseBody(response, responseBytes, signal)
      const raw = new TextDecoder('utf-8', { fatal: true }).decode(buffered)
      const fields = parseObjectFields(raw, responseBytes)
      if (
        url === `${base}/repositories/${this.configuration.repositoryID}` &&
        fields.get('id') !== String(this.configuration.repositoryID)
      ) {
        throw new GitHubAPIError('repository_scope_mismatch')
      }
      // Octokit's json-with-bigint preserves REST integers. Bootstrap callers
      // consume only validated token/repository fields. Keep original bytes.
      const responseHeaders = new Headers(response.headers)
      responseHeaders.delete('content-length')
      responseHeaders.delete('content-encoding')
      responseHeaders.set('content-type', 'application/json')
      return new Response(buffered, { status: response.status, headers: responseHeaders })
    } catch (error) {
      if (error instanceof ProviderResponseTooLargeError)
        throw new GitHubAPIError('response_too_large', { outcomeUnknown: mutation })
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
