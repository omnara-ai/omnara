import {
  type ChannelConnectorEventReceipt,
  type ChannelRegistrationTarget,
  type PublishChannelConnectorDefinitionRequest,
  schemas,
} from '@omnara/sdk'
import { z } from 'zod'

import type { CoreClient } from '../core-client'
import { isTransientCoreError } from '../core-http'
import type { OperationAttemptContext } from '../operation-retry'
import { ReceiptClientError } from '../receipt-http'
import {
  type ProviderWorkReservation,
  type ReceiptBehaviorContext,
  ReceiptBehaviorError,
} from '../types'
import { GatewayAtCapacityError } from '../work-budget'
import { type GitHubAuthentication, GitHubClient } from './client'
import { type GitHubConfiguration, githubConfiguration } from './configuration'
import { githubEvent, githubInputKey, githubPRRef } from './events'
import { githubInboundThread } from './inbound-thread'
import { githubWorkflowInput } from './input'
import { GitHubAPIError, githubSendParamsSchema, githubThreadParamsSchema } from './protocol'

export type GitHubBehaviorCore = Pick<
  CoreClient,
  | 'getAppConfiguration'
  | 'getInstallationConfiguration'
  | 'listRoutes'
  | 'publishDefinition'
  | 'lookupWorkflow'
  | 'deliverWorkflow'
>
export interface GitHubBehaviorOptions {
  core: GitHubBehaviorCore
  reserveWorkBytes(bytes: number): ProviderWorkReservation
  apiUrl?: string
  authenticationFor?(
    configuration: GitHubConfiguration,
    appRevision: number,
    installRevision: number,
  ): GitHubAuthentication
}
const route = schemas.zChannelConnectorRoute
  .extend({
    behavior_key: z.literal('github_pr'),
    configuration: z.strictObject({}),
  })
  .strict()

export function githubDefinition(
  kind: 'pr' | 'review_thread',
): PublishChannelConnectorDefinitionRequest {
  return {
    implementation_key: `github_${kind}`,
    kind: kind === 'pr' ? 'GITHUB_PR' : 'GITHUB_REVIEW_THREAD',
    description:
      kind === 'pr'
        ? 'A GitHub pull request conversation.'
        : 'A GitHub pull request review thread.',
    send_params_schema: kind === 'pr' ? githubSendParamsSchema : githubThreadParamsSchema,
    capabilities: {
      read: true,
      send: true,
      text: true,
      artifacts: false,
      permissions: false,
      questions: false,
      creates_reply_channel: kind === 'pr',
    },
  }
}

/** The default PR behavior has one configured route and one PR instance, even
 * when the first communication is a review-thread comment. Sender authority is
 * independent: no workflow ownership is used by outbound tool operations.
 */
export async function processGitHubEvent(
  receipt: Readonly<ChannelConnectorEventReceipt>,
  context: ReceiptBehaviorContext,
  options: GitHubBehaviorOptions,
): Promise<void> {
  const deadlineMs = Math.min(context.deadlineMs, Date.parse(receipt.lease_expires_at))
  const remaining = deadlineMs - Date.now()
  if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647)
    throw new ReceiptBehaviorError(true)
  const signal = AbortSignal.any([context.signal, AbortSignal.timeout(remaining)])
  const work = options.reserveWorkBytes(0)
  const nativeWork = options.reserveWorkBytes(0)
  try {
    const event = githubEvent.parse(receipt.payload)
    if (receipt.event_id !== event.delivery_id) throw new GitHubAPIError('event_scope_mismatch')
    const app = await options.core.getAppConfiguration(receipt.integration_app_id, signal)
    const install = await options.core.getInstallationConfiguration(
      receipt.integration_app_id,
      receipt.integration_install_id,
      signal,
    )
    if (
      app.app.id !== receipt.integration_app_id ||
      app.app.connector_key !== 'omnara' ||
      install.install.id !== receipt.integration_install_id ||
      install.install.provider_tenant_id !== event.installation_id ||
      install.install.provider_account_ref !== event.repository.id ||
      install.install.provider_identity.repository_node_id !== event.repository.node_id
    )
      throw new GitHubAPIError('event_scope_mismatch')
    const configuration = githubConfiguration(app, install)
    const routes = z
      .array(route)
      .max(1)
      .parse(await options.core.listRoutes(receipt, signal, work))
    const configured = routes[0]
    if (!configured) return
    const inputKey = githubInputKey(event)
    const instanceKey = githubPRRef(event)
    const lookup = await options.core.lookupWorkflow(
      receipt,
      {
        route_id: configured.id,
        instance_key: instanceKey,
        input_keys: [inputKey],
      },
      signal,
    )
    if (lookup.agent_state === 'archived' || lookup.input_keys.includes(inputKey)) return
    let thread: Awaited<ReturnType<typeof githubInboundThread>>
    // Preserve the signed observation in the inbox, but don't turn this App's
    // own published text into fresh instructions. No event authorizes mutations.
    if (event.event !== 'pull_request') {
      // Native responses are capped at 1MiB; include decoding/validation copies.
      nativeWork.resize(32 * 1024 * 1024)
      const client = new GitHubClient(
        configuration,
        options.apiUrl,
        options.authenticationFor?.(
          configuration,
          app.app.configuration_revision,
          install.install.configuration_revision,
        ),
      )
      const attempt: OperationAttemptContext = {
        requestId: receipt.receipt_id,
        attempt: 1,
        signal,
        deadlineMs,
      }
      const viewer = await client.viewerID(attempt)
      const author = event.comment?.user ?? event.review?.user ?? event.sender
      if (author.node_id === viewer) return
      if (event.event === 'pull_request_review_comment' && event.comment)
        thread = await githubInboundThread(
          client,
          event.pull_request.number,
          event.comment.node_id,
          attempt,
        )
    }
    const rootDefinition = await options.core.publishDefinition(
      receipt,
      githubDefinition('pr'),
      signal,
    )
    const root: ChannelRegistrationTarget = {
      definition_id: rootDefinition.id,
      provider_ref: instanceKey,
      provider_ref_kind: 'pr',
      display_name: `${event.repository.owner.login}/${event.repository.name}#${event.pull_request.number}`,
    }
    let target = root
    let threadStatus: string | undefined
    if (event.event === 'pull_request_review_comment' && event.comment) {
      if (thread) {
        const definition = await options.core.publishDefinition(
          receipt,
          githubDefinition('review_thread'),
          signal,
        )
        target = {
          definition_id: definition.id,
          provider_ref: `${instanceKey}:comment:${thread.rootID}`,
          provider_ref_kind: 'review_thread',
          display_name: `${root.display_name} review thread`,
          provider_metadata: { thread_id: thread.threadID },
          parent: root,
        }
        threadStatus = 'published'
      } else {
        // A deleted/unavailable native comment cannot authorize a new child.
        // Its original saved communication is still delivered on the PR.
        threadStatus = 'unavailable'
      }
    }
    const body = githubWorkflowInput(event)
    if (threadStatus) body.metadata = { ...body.metadata, thread_status: threadStatus }
    await options.core.deliverWorkflow(
      receipt,
      {
        ...body,
        route_id: configured.id,
        instance_key: instanceKey,
        target,
        grants: { read: true, send: true },
      },
      signal,
    )
  } catch (cause) {
    if (cause instanceof ReceiptBehaviorError) throw cause
    if (cause instanceof GitHubAPIError)
      throw new ReceiptBehaviorError(cause.retryable && !cause.outcomeUnknown, cause.retryAfterMs)
    if (cause instanceof ReceiptClientError)
      throw new ReceiptBehaviorError(
        cause.apiCode !== 'managed_work_admission_denied' &&
          (cause.code === 'transport_failed' ||
            cause.code === 'aborted' ||
            (cause.status !== undefined && [408, 409, 429].includes(cause.status)) ||
            (cause.status ?? 0) >= 500),
      )
    if (cause instanceof GatewayAtCapacityError || isTransientCoreError(cause) || signal.aborted)
      throw new ReceiptBehaviorError(true)
    throw new ReceiptBehaviorError(false)
  } finally {
    nativeWork.release()
    work.release()
  }
}
