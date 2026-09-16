import { type ChannelConnectorControlReceipt, schemas } from '@omnara/sdk'
import { z } from 'zod'

import type { CoreClient } from '../core/client'
import { ReceiptClientError } from '../core/receipt-http'
import type { ControlCompletion, ControlReceiptBehaviorContext } from '../types'
import { GitHubAppClient } from './app-client'
import { githubLifecycleEvent } from './events'
import { GitHubAPIError, githubNodeID } from './protocol'

export type GitHubProviderControl = Pick<
  CoreClient,
  'getAppConfiguration' | 'listInstallationControlScopes' | 'setInstallationProviderState'
>

/** Finite verified control work. A successful prefix survives native/core
 * failures; no observer refreshes a core fence around an older native result.
 */
export async function processGitHubControlReceipt(
  receipt: Readonly<ChannelConnectorControlReceipt>,
  context: ControlReceiptBehaviorContext,
  options: { core: GitHubProviderControl; apiUrl?: string },
): Promise<ControlCompletion> {
  let last = receipt.last_installation_id
  const progress = () =>
    last !== null && last !== receipt.last_installation_id ? { last_installation_id: last } : {}
  const deadlineMs = Math.min(context.deadlineMs, Date.parse(receipt.lease_expires_at)) - 1_000
  const remaining = deadlineMs - Date.now()
  if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647)
    return { outcome: 'retry', last_error: { code: 'control_deadline' } }
  const signal = AbortSignal.any([context.signal, AbortSignal.timeout(remaining)])
  const parsed = githubLifecycleEvent.safeParse(receipt.payload)
  if (
    !parsed.success ||
    parsed.data.delivery_id !== receipt.event_id ||
    parsed.data.installationID !== receipt.provider_tenant_id ||
    (receipt.end_installation_id === null && last !== null)
  )
    return { outcome: 'failed', last_error: { code: 'control_scope_mismatch' } }
  let coreWork
  let nativeWork
  try {
    coreWork = context.reserveWorkBytes(0)
    const app = await options.core.getAppConfiguration(receipt.integration_app_id, signal)
    if (
      !schemas.zChannelConnectorAppConfiguration.safeParse(app).success ||
      app.app.id !== receipt.integration_app_id ||
      app.app.provider !== 'github' ||
      app.app.connector_key !== 'omnara' ||
      app.app.provider_app_ref !== parsed.data.appID
    )
      return { outcome: 'failed', last_error: { code: 'control_scope_mismatch' } }
    if (receipt.end_installation_id === null) return { outcome: 'completed' }
    nativeWork = context.reserveWorkBytes(64 * 1024 * 1024)
    const credential = z
      .object({
        private_key: z
          .string()
          .min(1)
          .max(32 * 1024),
      })
      .parse(app.credential?.payload)
    if (app.credential?.kind !== 'integration_credentials')
      throw new GitHubAPIError('invalid_configuration')
    const native = new GitHubAppClient(
      app.app.provider_app_ref,
      credential.private_key,
      options.apiUrl,
    )
    const scopes = await options.core.listInstallationControlScopes(
      app.app.id,
      {
        provider_tenant_id: receipt.provider_tenant_id,
        limit: 20,
        after_installation_id: last ?? undefined,
        through_installation_id: receipt.end_installation_id,
      },
      signal,
      coreWork,
    )
    if (scopes.app_configuration_revision !== app.app.configuration_revision)
      throw new ReceiptClientError('http_error', 409)
    const session = scopes.installations.length
      ? await native.prepareInstallation(receipt.provider_tenant_id, {
          requestId: receipt.receipt_id,
          attempt: 1,
          signal,
          deadlineMs,
        })
      : null
    for (const scope of scopes.installations) {
      if (Date.now() >= deadlineMs || signal.aborted) {
        if (last !== receipt.last_installation_id) return { outcome: 'yield', ...progress() }
        return { outcome: 'retry', last_error: { code: 'control_deadline' } }
      }
      if (scope.provider_tenant_id !== receipt.provider_tenant_id)
        throw new ReceiptClientError('invalid_response')
      const repositoryID = githubNodeID.parse(scope.provider_identity.repository_node_id)
      const state = session === null ? 'disabled' : await session.repositoryState(repositoryID)
      try {
        await options.core.setInstallationProviderState(
          app.app.id,
          scope.id,
          {
            provider_tenant_id: receipt.provider_tenant_id,
            provider_account_ref: scope.provider_account_ref,
            expected_app_configuration_revision: scopes.app_configuration_revision,
            expected_configuration_revision: scope.configuration_revision,
            state,
          },
          signal,
        )
      } catch (cause) {
        if (!(cause instanceof ReceiptClientError && cause.status === 404)) throw cause
        // A setter 404 may hide an unavailable APP, not a deleted connection.
        // Re-list this bounded prefix through the same authority before skipping.
        const current = await options.core.listInstallationControlScopes(
          app.app.id,
          {
            provider_tenant_id: receipt.provider_tenant_id,
            limit: 1,
            after_installation_id: last ?? undefined,
            through_installation_id: scope.id,
          },
          signal,
          coreWork,
        )
        if (
          current.app_configuration_revision !== app.app.configuration_revision ||
          current.installations.length !== 0
        )
          throw cause
      }
      last = scope.id
    }
    if (scopes.next_after_installation_id === null) {
      // A complete bounded listing also proves any deleted tail is absent.
      last = receipt.end_installation_id
      return { outcome: 'completed', ...progress() }
    }
    if (last === receipt.last_installation_id) throw new ReceiptClientError('invalid_response')
    return { outcome: 'yield', ...progress() }
  } catch (cause) {
    const completion: ControlCompletion = {
      outcome: 'retry',
      ...progress(),
      last_error: {
        code: cause instanceof GitHubAPIError ? cause.code : 'control_observation_inconclusive',
      },
    }
    if (cause instanceof GitHubAPIError && cause.retryAfterMs !== undefined)
      completion.retry_after_ms = cause.retryAfterMs
    return completion
  } finally {
    nativeWork?.release()
    coreWork?.release()
  }
}
