import { createAppAuth } from '@octokit/auth-app'
import type { JsonBody } from '@omnara/sdk'
import { z } from 'zod'

import type { OperationAttemptContext } from '../operation-retry'
import { parseObjectFields } from '../operations-json'
import { readProviderResponseBody } from '../server-support'
import {
  type controlRepository,
  controlRepositoryQuery,
  githubInstallation,
  installationCanCommunicate,
  nativeControlID,
  repositoryObservation,
} from './control-protocol'
import { GitHubAPIError, githubDatabaseID, githubNodeID } from './protocol'
import { githubGraphQLRateLimit, githubRateLimitDelay } from './rate-limit'

export interface GitHubControlSession {
  repositoryState(nodeID: string): Promise<'active' | 'disabled'>
}

/** App-level control uses only native identity/permission reads and metadata-only
 * token minting. Sessions are local to one bounded page, never cached across claims.
 */
export class GitHubAppClient {
  private readonly auth: ReturnType<typeof createAppAuth>
  private readonly base: URL

  constructor(
    readonly appID: string,
    privateKey: string,
    apiUrl = 'https://api.github.com',
  ) {
    try {
      this.base = new URL(apiUrl.replace(/\/$/, ''))
      if (
        this.base.username ||
        this.base.password ||
        this.base.search ||
        this.base.hash ||
        (this.base.protocol !== 'https:' &&
          !(
            this.base.protocol === 'http:' &&
            ['localhost', '127.0.0.1', '[::1]'].includes(this.base.hostname)
          )) ||
        !nativeControlID.safeParse(appID).success
      )
        throw new Error('invalid endpoint or identity')
      this.auth = createAppAuth({ appId: appID, privateKey })
    } catch {
      throw new GitHubAPIError('invalid_configuration')
    }
  }

  /** Call only AFTER observing the whole page's core fences. Null means the
   * installation cannot currently communicate, never proof of repository deletion.
   * The returned session shares this claim's deadline and has no refresh/retry loop.
   */
  async prepareInstallation(
    installationID: string,
    context: OperationAttemptContext,
  ): Promise<GitHubControlSession | null> {
    if (!nativeControlID.safeParse(installationID).success)
      throw new GitHubAPIError('invalid_configuration')
    try {
      const jwt = (await this.auth({ type: 'app' })).token
      const app = z
        .object({ id: nativeControlID })
        .parse((await this.request('/app', jwt, context)).body)
      if (app.id !== this.appID) throw new GitHubAPIError('app_scope_mismatch')
      const current = await this.request(`/app/installations/${installationID}`, jwt, context)
      if (current.status === 404) return null
      const installation = githubInstallation.parse(current.body)
      if (installation.id !== installationID || installation.app_id !== this.appID)
        throw new GitHubAPIError('installation_scope_mismatch')
      if (!installationCanCommunicate(installation)) return null
      const token = z
        .object({
          token: z.string().min(1).max(4096),
          expires_at: z.iso.datetime(),
          permissions: z.object({ metadata: z.literal('read') }),
        })
        .parse(
          (
            await this.request(`/app/installations/${installationID}/access_tokens`, jwt, context, {
              permissions: { metadata: 'read' },
            })
          ).body,
        )
      if (Date.parse(token.expires_at) <= context.deadlineMs)
        throw new GitHubAPIError('invalid_response', { retryable: true })
      // Omitting repository selectors is intentional: this fresh token covers
      // all CURRENT selected repositories, so a narrowed token cannot hide one.
      return {
        repositoryState: async (nodeID) => {
          if (!githubNodeID.safeParse(nodeID).success)
            throw new GitHubAPIError('invalid_configuration')
          try {
            return await this.repositoryMembership(
              installationID,
              nodeID,
              jwt,
              token.token,
              context,
            )
          } catch (cause) {
            if (cause instanceof GitHubAPIError) throw cause
            throw new GitHubAPIError('provider_state_unavailable', { retryable: true })
          }
        },
      }
    } catch (cause) {
      if (cause instanceof GitHubAPIError) throw cause
      throw new GitHubAPIError('provider_state_unavailable', { retryable: true })
    }
  }

  private async repositoryMembership(
    installationID: string,
    nodeID: string,
    jwt: string,
    token: string,
    context: OperationAttemptContext,
  ): Promise<'active' | 'disabled'> {
    // One bounded address refetch. Never follow a provider URL or turn persistent
    // rename/transfer churn into a negative observation.
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const before = await this.repository(nodeID, token, context)
      if (before === null) return 'disabled'
      const path = `/repos/${encodeURIComponent(before.owner.login)}/${encodeURIComponent(before.name)}/installation`
      const current = await this.request(path, jwt, context)
      const after = await this.repository(nodeID, token, context)
      if (after === null) return 'disabled'
      if (
        before.name !== after.name ||
        before.owner.id !== after.owner.id ||
        before.owner.login !== after.owner.login ||
        current.status === 301
      )
        continue
      if (current.status === 404) return 'disabled'
      const installation = githubInstallation.parse(current.body)
      if (installation.app_id !== this.appID)
        throw new GitHubAPIError('installation_scope_mismatch')
      return installation.id === installationID && installationCanCommunicate(installation)
        ? 'active'
        : 'disabled'
    }
    throw new GitHubAPIError('repository_identity_unstable', { retryable: true })
  }

  private async repository(
    nodeID: string,
    token: string,
    context: OperationAttemptContext,
  ): Promise<z.infer<typeof controlRepository> | null> {
    const response = await this.request('/graphql', token, context, {
      query: controlRepositoryQuery,
      variables: { id: nodeID },
    })
    return repositoryObservation(response.body, nodeID)
  }

  private async request(
    path: string,
    token: string,
    context: OperationAttemptContext,
    body?: JsonBody,
  ): Promise<{ status: number; body: JsonBody | null }> {
    context.signal.throwIfAborted()
    const remaining = context.deadlineMs - Date.now()
    if (remaining <= 0 || remaining > 2_147_483_647)
      throw new GitHubAPIError('deadline_exceeded', { retryable: true })
    const signal = AbortSignal.any([context.signal, AbortSignal.timeout(remaining)])
    let response: Response | undefined
    try {
      response = await fetch(`${this.base.href.replace(/\/$/, '')}${path}`, {
        method: body ? 'POST' : 'GET',
        headers: {
          authorization: `Bearer ${token}`,
          accept: 'application/vnd.github+json',
          'content-type': 'application/json',
          'x-github-api-version': '2026-03-10',
        },
        body: body ? JSON.stringify(body) : undefined,
        signal,
        redirect: 'manual',
      })
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
      // A secondary-limit 403 need not include Retry-After or exhaust primary
      // quota. All control 403s already retry; conservatively give them GitHub's
      // minimum delay without parsing/retaining an arbitrary error body.
      if (response.status === 403)
        throw new GitHubAPIError('provider_state_unavailable', {
          retryable: true,
          retryAfterMs: githubRateLimitDelay(response.headers),
        })
      if (
        !body &&
        ((response.status === 404 && path !== '/app') ||
          (response.status === 301 && path.startsWith('/repos/')))
      )
        return { status: response.status, body: null }
      if (!response.ok) throw new GitHubAPIError('provider_state_unavailable', { retryable: true })
      const bytes = await readProviderResponseBody(response, 2 * 1024 * 1024, signal)
      const raw = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
      parseObjectFields(raw, 2 * 1024 * 1024)
      const parsed = z.json().parse(
        JSON.parse(raw, (key: string, value: JsonBody, source?: { source?: string }) => {
          if (path !== '/graphql' && (key === 'id' || key === 'app_id'))
            return githubDatabaseID.parse(source?.source)
          return value
        }),
      )
      if (path === '/graphql') {
        const errors = z
          .object({
            errors: z.array(
              z.object({ type: z.string().optional(), message: z.string().optional() }),
            ),
          })
          .safeParse(parsed)
        const rateLimit = errors.success
          ? githubGraphQLRateLimit(errors.data.errors, response.headers)
          : undefined
        if (rateLimit)
          throw new GitHubAPIError('rate_limited', {
            retryable: true,
            retryAfterMs: githubRateLimitDelay(
              response.headers,
              Date.now(),
              rateLimit === 'primary',
            ),
          })
      }
      return { status: response.status, body: parsed }
    } catch (cause) {
      if (cause instanceof GitHubAPIError) throw cause
      throw new GitHubAPIError('provider_state_unavailable', { retryable: true })
    } finally {
      if (response && !response.bodyUsed) await response.body?.cancel().catch(() => undefined)
    }
  }
}
