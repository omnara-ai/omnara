import {
  ApiError,
  type ChannelConnectorCapability,
  type ChannelOperationDestination,
  schemas,
} from '@omnara/sdk'
import { z } from 'zod'

import type { CoreClient } from '../core/client'
import { ReceiptClientError } from '../core/receipt-http'
import { isTransientCoreError } from '../core/requests'
import { parseObjectFields } from '../json'
import { maxOperationPayloadBytes } from '../operations/envelope'
import type { OperationsOptions } from '../operations/handler'
import { OperationRetryError } from '../operations/retry'
import { type ReceiptBehavior, ReceiptBehaviorError } from '../types'
import { GatewayAtCapacityError, type WorkByteBudget } from '../work-budget'
import { resolveSlackAddress, SlackAddressError } from './address'
import { processSlackEvent } from './behavior'
import { SlackAPIError, SlackClient } from './client'
import { slackCredentials } from './configuration'
import { parseSlackMessage } from './messages'
import { readSlackOperation, sendSlackOperation, slackDestination } from './operations'
import { sendSlackInteraction } from './prompts'

export const slackCapability: Readonly<ChannelConnectorCapability> = {
  connector_key: 'omnara',
  provider: 'slack',
}

export interface SlackGatewayOptions {
  core: Pick<
    CoreClient,
    | 'getAppConfiguration'
    | 'getInstallationConfiguration'
    | 'listRoutes'
    | 'publishDefinition'
    | 'lookupRecipients'
    | 'deliverInput'
    | 'lookupWorkflow'
    | 'deliverWorkflow'
  >
  workBudget: WorkByteBudget
  /** Trusted deployment/test endpoint. Event, route and tool data cannot set it. */
  apiUrl?: string
}

const routeSchema = schemas.zChannelConnectorRoute
  .extend({
    behavior_key: z.literal('slack_conversation'),
    configuration: z.strictObject({}),
  })
  .strict()
const destinationSchema = schemas.zChannelOperationDestination.strict()
const sendSchema = schemas.zChannelSendOperation
  .extend({
    destination: destinationSchema,
    // The generated send-message intersection permits unknown fields; the actual
    // text/artifact declaration is closed, with nonempty content checked by sender.
    message: schemas.zChannelMessage.strict(),
    reply_channel_grants: schemas.zChannelGrants.strict().optional(),
  })
  .strict()
const readSchema = schemas.zChannelReadOperation.extend({ destination: destinationSchema }).strict()
const interactionSchema = schemas.zChannelInteractionOperation
  .extend({ destination: destinationSchema })
  .strict()
const resolveSchema = schemas.zChannelResolveAddressOperation.strict()

/** Concrete composition callbacks only. Core retains Slack's verified public
 * intake; no duplicate webhook, SDK dedupe, runtime send shim, or outgoing queue.
 */
export function createSlackGateway(options: SlackGatewayOptions) {
  const { core, workBudget, apiUrl } = options
  const processReceipt: ReceiptBehavior = async (receipt, context) => {
    try {
      await processSlackEvent(receipt, {
        ...context,
        apiUrl,
        reserveWorkBytes: workBudget.reserve,
        getInstallation: async (queued, signal) => {
          const install = await core
            .getInstallationConfiguration(
              queued.integration_app_id,
              queued.integration_install_id,
              signal,
            )
            .catch((cause: unknown) => {
              if (isTransientCoreError(cause)) throw new ReceiptBehaviorError(true)
              throw cause
            })
          if (!schemas.zChannelConnectorInstallationConfiguration.safeParse(install).success)
            throw new SlackAPIError('invalid_configuration')
          return install
        },
        listRoutes: async (queued, signal) => {
          const work = workBudget.reserve(0)
          try {
            const routes = await core.listRoutes(queued, signal, work)
            // Retain the fetch/parser charge until opaque configurations have
            // been checked and projected to the small supported route facts.
            const parsed = z.array(routeSchema).max(64).safeParse(routes)
            if (!parsed.success) throw new SlackAPIError('unsupported_route_configuration')
            return parsed.data.map((route) => ({
              route_id: route.id,
              grants: { read: true, send: true },
            }))
          } finally {
            work.release()
          }
        },
        publishDefinition: (queued, body, signal) => core.publishDefinition(queued, body, signal),
        lookupRecipients: (queued, body, signal) => core.lookupRecipients(queued, body, signal),
        deliverInput: (queued, body, signal) => core.deliverInput(queued, body, signal),
        lookupWorkflow: (queued, body, signal) => core.lookupWorkflow(queued, body, signal),
        deliverWorkflow: (queued, body, signal) => core.deliverWorkflow(queued, body, signal),
      })
    } catch (cause) {
      // Slack behavior performs provider reads and core-idempotent admission.
      // Retry only recognized transient failures; unknown exceptions stay unknown.
      if (cause instanceof SlackAPIError)
        throw new ReceiptBehaviorError(cause.retryable && !cause.outcomeUnknown)
      if (cause instanceof ReceiptClientError) {
        const retryable =
          cause.apiCode !== 'managed_work_admission_denied' &&
          (cause.code === 'transport_failed' ||
            cause.code === 'aborted' ||
            (cause.code === 'http_error' && retryableCoreStatus(cause.status)))
        throw new ReceiptBehaviorError(retryable)
      }
      if (cause instanceof ApiError)
        throw new ReceiptBehaviorError(retryableCoreStatus(cause.status))
      if (cause instanceof GatewayAtCapacityError) throw new ReceiptBehaviorError(true)
      throw cause
    }
  }

  const executeOperation: OperationsOptions['execute'] = async (
    operation,
    artifacts,
    parentSignal,
  ) => {
    const remaining = operation.deadlineMs - Date.now()
    if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647)
      throw new OperationRetryError('deadline_exceeded', false, 0)
    if (parentSignal.aborted) throw new OperationRetryError('canceled', false, 0)
    const controller = new AbortController()
    const timer = setTimeout(() => {
      controller.abort()
    }, remaining)
    const signal = AbortSignal.any([parentSignal, controller.signal])
    let dispatchStarted = false
    try {
      if (
        operation.capability.connector_key !== slackCapability.connector_key ||
        operation.capability.provider !== slackCapability.provider
      )
        throw new SlackAPIError('unsupported_capability')
      // The transport already checks these bytes. Keep the exported callback safe
      // when used directly too: duplicate keys cannot disappear in JSON.parse.
      parseObjectFields(operation.payloadJSON, maxOperationPayloadBytes)
      const raw: unknown = JSON.parse(operation.payloadJSON)
      const parsed =
        operation.kind === 'resolve_address'
          ? resolveSchema.safeParse(raw)
          : operation.kind === 'send'
            ? sendSchema.safeParse(raw)
            : operation.kind === 'read'
              ? readSchema.safeParse(raw)
              : interactionSchema.safeParse(raw)
      if (!parsed.success) {
        if (operation.kind === 'resolve_address') throw new SlackAddressError('invalid_address')
        throw new SlackAPIError('invalid_operation_payload')
      }
      if ('destination' in parsed.data) validateImplementation(parsed.data.destination)
      if (operation.kind !== 'send' && (artifacts.length || operation.artifacts.length))
        throw new SlackAPIError('unexpected_artifacts')
      const app = await core.getAppConfiguration(operation.scope.integration_app_id, signal)
      if (
        !schemas.zChannelConnectorAppConfiguration.safeParse(app).success ||
        app.app.id !== operation.scope.integration_app_id ||
        app.app.provider !== slackCapability.provider ||
        app.app.connector_key !== slackCapability.connector_key
      )
        throw new SlackAPIError('operation_scope_mismatch')
      const install = await core.getInstallationConfiguration(
        operation.scope.integration_app_id,
        operation.scope.integration_install_id,
        signal,
      )
      if (
        !schemas.zChannelConnectorInstallationConfiguration.safeParse(install).success ||
        install.integration_app_id !== operation.scope.integration_app_id ||
        install.install.id !== operation.scope.integration_install_id ||
        install.install.project_id !== operation.scope.project_id ||
        install.install.provider_account_ref !== app.app.provider_app_ref ||
        install.app_configuration_revision !== app.app.configuration_revision
      )
        throw new SlackAPIError('operation_scope_mismatch')
      const credentials = slackCredentials(install)
      const client = new SlackClient(credentials.botToken, apiUrl)
      const retry = { requestId: operation.requestId, deadlineMs: operation.deadlineMs, signal }
      signal.throwIfAborted()
      switch (operation.kind) {
        case 'resolve_address': {
          const input = resolveSchema.parse(raw)
          dispatchStarted = true
          const payload = await resolveSlackAddress(
            client,
            input,
            {
              installation: operation.scope,
              teamId: install.install.provider_tenant_id ?? '',
              publishDefinition: (scope, body, signal) =>
                core.publishDefinition(scope, body, signal),
            },
            retry,
          )
          return {
            outcome: 'completed',
            payload: z.json().parse(JSON.parse(JSON.stringify(payload))),
          }
        }
        case 'send': {
          const input = sendSchema.parse(raw)
          dispatchStarted = true
          const payload = await sendSlackOperation(client, input, artifacts, 'slack_thread', retry)
          return {
            outcome: 'completed',
            payload: z.json().parse(JSON.parse(JSON.stringify(payload))),
          }
        }
        case 'read': {
          const input = readSchema.parse(raw)
          dispatchStarted = true
          const payload = await readSlackOperation(client, parseSlackMessage, input, retry)
          return {
            outcome: 'completed',
            payload: z.json().parse(JSON.parse(JSON.stringify(payload))),
          }
        }
        case 'interaction': {
          const input = interactionSchema.parse(raw)
          if (
            input.agent_id !== operation.scope.agent_id ||
            input.channel_id !== operation.scope.channel_id
          )
            throw new SlackAPIError('interaction_scope_mismatch')
          dispatchStarted = true
          const payload = await sendSlackInteraction(client, input, retry)
          return {
            outcome: 'completed',
            payload: z.json().parse(JSON.parse(JSON.stringify(payload))),
          }
        }
      }
    } catch (cause) {
      // Provider helpers own one bounded retry loop and classify publication.
      // OperationsHandler maps those errors to failed/unknown without resending.
      if (operation.kind === 'resolve_address') {
        return cause instanceof SlackAddressError
          ? { outcome: 'failed', payload: { code: cause.code } }
          : { outcome: 'failed' }
      }
      if (cause instanceof OperationRetryError) throw cause
      if (!dispatchStarted || (cause instanceof SlackAPIError && !cause.outcomeUnknown))
        return { outcome: 'failed' }
      throw cause
    } finally {
      clearTimeout(timer)
      controller.abort()
    }
  }
  return { processReceipt, executeOperation }
}

function validateImplementation(destination: ChannelOperationDestination): void {
  const thread =
    destination.implementation_key === 'slack_thread' && destination.provider_ref_kind === 'thread'
  const channel =
    destination.implementation_key === 'slack_channel' &&
    destination.provider_ref_kind === 'channel'
  const dm = destination.implementation_key === 'slack_dm' && destination.provider_ref_kind === 'dm'
  if (!thread && !channel && !dm) throw new SlackAPIError('unsupported_implementation')
  slackDestination(destination)
}

function retryableCoreStatus(status: number | undefined): boolean {
  return (
    status !== undefined && (status === 408 || status === 409 || status === 429 || status >= 500)
  )
}
