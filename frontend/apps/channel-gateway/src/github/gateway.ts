import { type ChannelConnectorCapability, schemas } from '@omnara/sdk'
import { z } from 'zod'

import type { CoreClient } from '../core-client'
import { OperationRetryError } from '../operation-retry'
import type { OperationsOptions } from '../operations'
import { maxOperationPayloadBytes, parseObjectFields } from '../operations-json'
import { GitHubClient } from './client'
import { githubConfiguration } from './configuration'
import { GitHubOperationError, readGitHubOperation, sendGitHubOperation } from './operations'
import { GitHubAPIError } from './protocol'
import { GitHubAddressError, resolveGitHubAddress } from './resolve'

export const githubCapability: Readonly<ChannelConnectorCapability> = {
  connector_key: 'omnara',
  provider: 'github',
}

export interface GitHubGatewayOptions {
  core: Pick<
    CoreClient,
    | 'getAppConfiguration'
    | 'getInstallationConfiguration'
    | 'lookupGitHubReviews'
    | 'recordGitHubReview'
    | 'publishDefinition'
  >
  /** Trusted deployment/test endpoint; never taken from a tool, event or route. */
  apiUrl?: string
}

const resolveSchema = schemas.zChannelResolveAddressOperation.strict()
const destinationSchema = schemas.zChannelOperationDestination.strict()
const sendSchema = schemas.zChannelSendOperation
  .extend({
    destination: destinationSchema,
    message: schemas.zChannelMessage.strict(),
    reply_channel_grants: schemas.zChannelGrants.strict().optional(),
  })
  .strict()
const readSchema = schemas.zChannelReadOperation.extend({ destination: destinationSchema }).strict()

/** Synchronous operations are independent of webhook/receipt runtime ownership.
 * No provider cleanup or queue is created when this callback finishes or aborts.
 */
export function createGitHubGateway(options: GitHubGatewayOptions) {
  const executeOperation: OperationsOptions['execute'] = async (
    operation,
    artifacts,
    parentSignal,
  ) => {
    const remaining = operation.deadlineMs - Date.now()
    if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647)
      throw new OperationRetryError('deadline_exceeded', false, 0)
    if (parentSignal.aborted) throw new OperationRetryError('canceled', false, 0)
    const deadline = new AbortController()
    const timer = setTimeout(() => {
      deadline.abort()
    }, remaining)
    const signal = AbortSignal.any([parentSignal, deadline.signal])
    let dispatched = false
    try {
      if (
        operation.capability.connector_key !== githubCapability.connector_key ||
        operation.capability.provider !== githubCapability.provider
      )
        throw new GitHubAPIError('unsupported_capability')
      // GitHub permission/question interactions stay in Omnara UI.
      if (operation.kind === 'interaction') throw new GitHubAPIError('unsupported_interaction')
      if (artifacts.length || operation.artifacts.length)
        throw new GitHubAPIError('unexpected_artifacts')
      parseObjectFields(operation.payloadJSON, maxOperationPayloadBytes)
      const raw: unknown = JSON.parse(operation.payloadJSON)
      const parsed =
        operation.kind === 'resolve_address'
          ? resolveSchema.safeParse(raw)
          : operation.kind === 'send'
            ? sendSchema.safeParse(raw)
            : readSchema.safeParse(raw)
      if (!parsed.success) {
        if (operation.kind === 'resolve_address') throw new GitHubAddressError('invalid_address')
        throw new GitHubAPIError('invalid_operation_payload')
      }

      const app = await options.core.getAppConfiguration(operation.scope.integration_app_id, signal)
      if (
        !schemas.zChannelConnectorAppConfiguration.safeParse(app).success ||
        app.app.id !== operation.scope.integration_app_id ||
        app.app.provider !== githubCapability.provider ||
        app.app.connector_key !== githubCapability.connector_key
      )
        throw new GitHubAPIError('operation_scope_mismatch')
      const install = await options.core.getInstallationConfiguration(
        operation.scope.integration_app_id,
        operation.scope.integration_install_id,
        signal,
      )
      if (
        !schemas.zChannelConnectorInstallationConfiguration.safeParse(install).success ||
        install.install.id !== operation.scope.integration_install_id ||
        install.install.project_id !== operation.scope.project_id
      )
        throw new GitHubAPIError('operation_scope_mismatch')
      // Configuration checks app identity/revision and the exact selected repo.
      // Construct per operation so key rotation cannot leave an old token cache.
      const client = new GitHubClient(githubConfiguration(app, install), options.apiUrl)
      signal.throwIfAborted()
      const retry = { requestId: operation.requestId, deadlineMs: operation.deadlineMs, signal }
      dispatched = true
      if (operation.kind === 'resolve_address') {
        const result = await resolveGitHubAddress(
          client,
          resolveSchema.parse(raw),
          {
            installation: operation.scope,
            publishDefinition: (scope, body, signal) =>
              options.core.publishDefinition(scope, body, signal),
          },
          retry,
        )
        return { outcome: 'completed', payload: z.json().parse(JSON.parse(JSON.stringify(result))) }
      }
      const result =
        operation.kind === 'send'
          ? await sendGitHubOperation(
              client,
              options.core,
              sendSchema.parse(raw),
              operation.scope,
              retry,
            )
          : await readGitHubOperation(
              client,
              options.core,
              readSchema.parse(raw),
              operation.scope,
              retry,
            )
      return { outcome: 'completed', payload: z.json().parse(JSON.parse(JSON.stringify(result))) }
    } catch (cause) {
      if (operation.kind === 'resolve_address')
        return cause instanceof GitHubAddressError
          ? { outcome: 'failed', payload: { code: cause.code } }
          : { outcome: 'failed' }
      if (cause instanceof GitHubOperationError)
        return {
          outcome: operation.kind === 'send' && cause.outcomeUnknown ? 'unknown' : 'failed',
          payload: cause.payload,
        }
      if (cause instanceof OperationRetryError) throw cause
      if (!dispatched || (cause instanceof GitHubAPIError && !cause.outcomeUnknown))
        return { outcome: 'failed' }
      throw cause
    } finally {
      clearTimeout(timer)
      deadline.abort()
    }
  }
  return { executeOperation }
}
