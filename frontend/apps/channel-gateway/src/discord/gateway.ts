import {
  type ChannelConnectorCapability,
  type ChannelSendOperationResult,
  schemas,
} from '@omnara/sdk'
import { z } from 'zod'

import type { CoreClient } from '../core-client'
import { OperationRetryError } from '../operation-retry'
import type { OperationsOptions } from '../operations'
import { maxOperationPayloadBytes, parseObjectFields } from '../operations-json'
import {
  DiscordAddressError,
  discordDefinition,
  discordDestination,
  resolveDiscordAddress,
} from './address'
import { DiscordClient } from './client'
import { discordConfiguration } from './configuration'
import { sendDiscordMessage } from './messages'
import { DiscordAPIError } from './protocol'
import { readDiscordOperation } from './read'

export const discordCapability: Readonly<ChannelConnectorCapability> = {
  connector_key: 'omnara',
  provider: 'discord',
}

export interface DiscordGatewayOptions {
  core: Pick<
    CoreClient,
    'getAppConfiguration' | 'getInstallationConfiguration' | 'publishDefinition'
  >
  /** Trusted deployment/test override; never supplied by tool or provider data. */
  apiUrl?: string
}

const destinationSchema = schemas.zChannelOperationDestination.strict()
const sendSchema = schemas.zChannelSendOperation
  .extend({
    destination: destinationSchema,
    message: schemas.zChannelMessage.strict(),
    reply_channel_grants: schemas.zChannelGrants.strict().optional(),
  })
  .strict()
const readSchema = schemas.zChannelReadOperation.extend({ destination: destinationSchema }).strict()
const resolveSchema = schemas.zChannelResolveAddressOperation.strict()

/** Native operations use live scope/configuration and one bounded retry budget.
 * Socket intake and receipt behavior are separate from these synchronous sends.
 */
export function createDiscordGateway(options: DiscordGatewayOptions) {
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
        operation.capability.connector_key !== discordCapability.connector_key ||
        operation.capability.provider !== discordCapability.provider
      )
        throw new DiscordAPIError('unsupported_capability')
      // Unsupported interactions fail before any provider call. The definitions
      // advertise dashboard fallback, not partially implemented forms.
      if (operation.kind === 'interaction') throw new DiscordAPIError('unsupported_interaction')
      parseObjectFields(operation.payloadJSON, maxOperationPayloadBytes)
      const raw: unknown = JSON.parse(operation.payloadJSON)
      const schema =
        operation.kind === 'send'
          ? sendSchema
          : operation.kind === 'read'
            ? readSchema
            : resolveSchema
      const parsed = schema.safeParse(raw)
      if (!parsed.success) {
        if (operation.kind === 'resolve_address') throw new DiscordAddressError('invalid_address')
        throw new DiscordAPIError('invalid_operation_payload')
      }
      if ('destination' in parsed.data) discordDestination(parsed.data.destination)
      if (operation.kind !== 'send' && (artifacts.length || operation.artifacts.length))
        throw new DiscordAPIError('unexpected_artifacts')
      const app = await options.core.getAppConfiguration(operation.scope.integration_app_id, signal)
      if (
        !schemas.zChannelConnectorAppConfiguration.safeParse(app).success ||
        app.app.id !== operation.scope.integration_app_id
      )
        throw new DiscordAPIError('operation_scope_mismatch')
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
        throw new DiscordAPIError('operation_scope_mismatch')
      const client = new DiscordClient(discordConfiguration(app, install), options.apiUrl)
      const retry = { requestId: operation.requestId, deadlineMs: operation.deadlineMs, signal }
      if (operation.kind === 'send') {
        const send = sendSchema.parse(raw)
        const grants = send.reply_channel_grants
        if (
          send.destination.implementation_key === 'discord_channel' &&
          grants &&
          (grants.receive || grants.read || grants.send)
        )
          // A freshly registered parent may be this installation's first channel.
          // Completion needs the child definition before any native publication.
          await options.core.publishDefinition(operation.scope, discordDefinition('thread'), signal)
      }
      signal.throwIfAborted()
      dispatched = true
      switch (operation.kind) {
        case 'resolve_address': {
          const payload = await resolveDiscordAddress(
            client,
            resolveSchema.parse(raw),
            {
              installation: operation.scope,
              publishDefinition: (scope, body, attemptSignal) =>
                options.core.publishDefinition(scope, body, attemptSignal),
            },
            retry,
          )
          return {
            outcome: 'completed',
            payload: z.json().parse(JSON.parse(JSON.stringify(payload))),
          }
        }
        case 'read': {
          const payload = await readDiscordOperation(client, readSchema.parse(raw), retry)
          return {
            outcome: 'completed',
            payload: z.json().parse(JSON.parse(JSON.stringify(payload))),
          }
        }
        case 'send': {
          const published = await sendDiscordMessage(
            client,
            sendSchema.parse(raw),
            artifacts,
            retry,
          )
          const payload: ChannelSendOperationResult = {
            publication: 'published',
            message_channel: 'destination',
            message_id: published.message.id,
            created_at: new Date(published.message.timestamp).toISOString(),
          }
          if (published.thread)
            payload.reply_channel = {
              implementation_key: 'discord_thread',
              provider_ref: published.thread.id,
              provider_ref_kind: 'thread',
              display_name: published.thread.name,
            }
          else if (published.continuationError)
            payload.continuation_error = published.continuationError
          return {
            outcome: 'completed',
            payload: z.json().parse(JSON.parse(JSON.stringify(payload))),
          }
        }
      }
    } catch (cause) {
      if (operation.kind === 'resolve_address')
        return cause instanceof DiscordAddressError
          ? { outcome: 'failed', payload: { code: cause.code } }
          : { outcome: 'failed' }
      if (cause instanceof OperationRetryError) throw cause
      if (!dispatched || (cause instanceof DiscordAPIError && !cause.outcomeUnknown))
        return { outcome: 'failed' }
      throw cause
    } finally {
      clearTimeout(timer)
      deadline.abort()
    }
  }
  return { executeOperation }
}
