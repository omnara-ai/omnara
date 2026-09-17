import type {
  ChannelConnectorEventReceipt,
  ChannelConnectorInputResponse,
  ChannelConnectorInstallationConfiguration,
  ChannelConnectorRecipient,
  ChannelDefinition,
  DeliverChannelConnectorWorkflowRequest,
  LookupChannelConnectorRecipientsResponse,
  LookupChannelConnectorWorkflowRequest,
  LookupChannelConnectorWorkflowResponse,
  PublishChannelConnectorDefinitionRequest,
} from '@omnara/sdk'

import type {
  ReceiptInputRequest,
  ReceiptRecipientsRequest,
  ReceiptWorkflowRequest,
} from '../core/client'
import { ReceiptClientError } from '../core/receipt-http'
import type { OperationAttemptContext } from '../operations/retry'
import type { ProviderWorkReservation, ReceiptBehaviorContext } from '../types'
import { SlackClient } from './client'
import { slackCredentials } from './configuration'
import { enrichSlackInput, type SlackEnrichment } from './enrichment'
import { SlackAPIError } from './errors'
import {
  displayMetadata,
  parseSlackReceipt,
  slackChannelDisplayName,
  slackInboundRoute,
  slackInputKeys,
  slackInputText,
} from './events'
import { prepareSlackFiles, type SlackInputFiles } from './files'
import { applySlackInputEffects, sendSlackLaunchDenial } from './input-effects'

export type SlackWorkflowRoute = Pick<DeliverChannelConnectorWorkflowRequest, 'route_id' | 'grants'>

/** Concrete core wiring stays with the factory. All identifiers, route grants and
 * leases originate in core; this seam does not select profiles or mint authority.
 */
export interface SlackBehaviorContext extends ReceiptBehaviorContext {
  getInstallation(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    signal: AbortSignal,
  ): Promise<ChannelConnectorInstallationConfiguration>
  listRoutes(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    signal: AbortSignal,
  ): Promise<readonly SlackWorkflowRoute[]>
  publishDefinition(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    body: PublishChannelConnectorDefinitionRequest,
    signal: AbortSignal,
  ): Promise<ChannelDefinition>
  lookupRecipients(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    body: ReceiptRecipientsRequest,
    signal: AbortSignal,
  ): Promise<LookupChannelConnectorRecipientsResponse>
  deliverInput(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    body: ReceiptInputRequest,
    signal: AbortSignal,
  ): Promise<ChannelConnectorInputResponse>
  lookupWorkflow(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    body: LookupChannelConnectorWorkflowRequest,
    signal: AbortSignal,
  ): Promise<LookupChannelConnectorWorkflowResponse>
  deliverWorkflow(
    receipt: Readonly<ChannelConnectorEventReceipt>,
    body: ReceiptWorkflowRequest,
    signal: AbortSignal,
  ): Promise<ChannelConnectorInputResponse>
  reserveWorkBytes(bytes: number): ProviderWorkReservation
  /** Trusted deployment/test configuration, never read from an event. */
  apiUrl?: string
}

/** These describe provider support only. Setup supplies grants independently. */
export function slackDefinition(
  kind: 'channel' | 'dm' | 'thread',
): PublishChannelConnectorDefinitionRequest {
  return {
    implementation_key: `slack_${kind}`,
    kind: kind === 'thread' ? 'SLACK_THREAD' : 'SLACK_CHANNEL',
    description:
      kind === 'thread'
        ? 'A Slack message thread.'
        : kind === 'dm'
          ? 'A persistent Slack direct message.'
          : 'A Slack channel.',
    send_params_schema: { type: 'object', properties: {}, additionalProperties: false },
    capabilities: {
      read: true,
      send: true,
      text: true,
      artifacts: true,
      permissions: true,
      questions: true,
      creates_reply_channel: kind === 'channel',
    },
  }
}

/** Process a verified saved callback using core's durable receipt identity.
 * The receipt consumer alone completes/retries the receipt after all routes.
 */
export async function processSlackEvent(
  receipt: Readonly<ChannelConnectorEventReceipt>,
  context: SlackBehaviorContext,
): Promise<void> {
  const envelope = parseSlackReceipt(receipt)
  if (!envelope) return
  const deadlineMs = Math.min(context.deadlineMs, Date.parse(receipt.lease_expires_at))
  const remaining = deadlineMs - Date.now()
  if (!Number.isSafeInteger(remaining) || remaining <= 0 || remaining > 2_147_483_647)
    throw new SlackAPIError('deadline_exceeded')
  context.signal.throwIfAborted()
  const controller = new AbortController()
  const timer = setTimeout(() => {
    controller.abort()
  }, remaining)
  const signal = AbortSignal.any([context.signal, controller.signal])
  let work: ProviderWorkReservation | undefined
  try {
    const install = await context.getInstallation(receipt, signal)
    if (
      install.integration_app_id !== receipt.integration_app_id ||
      install.install.id !== receipt.integration_install_id ||
      install.install.provider_account_ref !== envelope.api_app_id ||
      install.install.provider_tenant_id !== envelope.team_id
    )
      throw new SlackAPIError('event_scope_mismatch')
    const credentials = slackCredentials(install)
    const event = envelope.event
    if (
      !envelope.authorizations.some(
        (auth) =>
          auth.team_id === envelope.team_id &&
          auth.user_id === credentials.botUserId &&
          auth.is_bot,
      )
    )
      return
    if (
      !event.user ||
      event.user === credentials.botUserId ||
      event.user === 'USLACKBOT' ||
      event.bot_id ||
      [event.team, event.source_team, event.user_team].some(
        (team) => team && team !== envelope.team_id,
      )
    )
      return
    const route = slackInboundRoute(event, credentials.botUserId)
    if (!route) return
    const client = new SlackClient(credentials.botToken, context.apiUrl)
    const attempt: OperationAttemptContext = {
      requestId: receipt.receipt_id,
      deadlineMs,
      signal,
      attempt: 1,
    }
    const keys = slackInputKeys(envelope.team_id, event)
    let definition: ChannelDefinition | undefined
    const enrichments = new Map<boolean, SlackEnrichment>()
    let files: SlackInputFiles | undefined
    const completed = new Set<string>()
    const presentations = new Map<string, string>()
    let conflict: ReceiptClientError | undefined
    // Re-observe routing and semantic keys after each known conflict. Four
    // attempts bound mapping/key races; identical failed presentations stop early.
    for (let render = 0; render < 4; render += 1) {
      let retry = false
      for await (const target of inputTargets(receipt, context, route.providerRef, keys, signal)) {
        signal.throwIfAborted()
        const lookup =
          target.kind === 'recipient'
            ? { exists: true, input_keys: target.recipient.input_keys, agent_state: undefined }
            : target.lookup
        const identity =
          target.kind === 'recipient' ? target.recipient.agent_id : target.configured.route_id
        if (completed.has(identity)) continue
        if (lookup.agent_state === 'archived' || (route.appendOnly && !lookup.exists)) continue
        const ownExists = lookup.input_keys.includes(keys.own)
        const siblingExists = lookup.input_keys.includes(keys.sibling)
        const hasFiles = event.files.length > 0
        const replay = ownExists || (!hasFiles && siblingExists)
        const inputKey = !hasFiles && siblingExists ? keys.sibling : keys.own
        const precondition = replay ? undefined : { input_key: keys.sibling, exists: siblingExists }
        const newlyMapped = !lookup.exists
        const presentation = JSON.stringify([
          target.kind === 'recipient' ? target.recipient.binding_id : identity,
          inputKey,
          precondition,
          route.kind === 'thread' && newlyMapped,
          replay,
        ])
        if (conflict && presentation === presentations.get(identity)) throw conflict
        const fetchHistory = route.kind === 'thread' && newlyMapped
        let enrichment = enrichments.get(fetchHistory)
        if (!enrichment && !replay) {
          enrichment = await enrichSlackInput(
            client,
            event,
            fetchHistory,
            install,
            credentials.botUserId,
            attempt,
          )
          enrichments.set(fetchHistory, enrichment)
        }
        if (!files && hasFiles && !replay) {
          work ??= context.reserveWorkBytes(0)
          files = await prepareSlackFiles(client, event.files, attempt, work)
        }
        const labels = enrichment?.labels ?? {
          users: new Map<string, string>(),
          channels: new Map<string, string>(),
        }
        const history = newlyMapped ? (enrichment?.history ?? '') : ''
        const blocks = slackInputText(
          event,
          route,
          newlyMapped,
          history,
          labels,
          hasFiles && siblingExists,
        )
        if (files?.summary && !replay)
          blocks.push({
            type: 'text',
            text: `\n${files.summary}`,
            metadata: displayMetadata(`\n${files.summary}`, files.summary),
          })
        if (!replay && files) blocks.push(...files.blocks)
        const body: Omit<ReceiptInputRequest, 'binding_id'> = {
          input_key: inputKey,
          input_precondition: precondition,
          author: { ref: event.user, display_name: labels.users.get(event.user) ?? '' },
          content_blocks: blocks,
          metadata: {
            provider: 'slack',
            event_id: envelope.event_id,
            event_type: event.type,
            event_subtype: event.subtype,
            channel: event.channel,
            channel_type: event.channel_type,
            message_ts: event.ts,
            provider_ref: route.providerRef,
            history_status: newlyMapped ? (enrichment?.historyStatus ?? 'skipped') : 'skipped',
          },
          delivery_mode: 'steering',
          cancel_open_interactions: true,
        }
        if (files && !replay && body.metadata) body.metadata.files = files.metadata
        try {
          let delivered: ChannelConnectorInputResponse
          if (target.kind === 'recipient') {
            delivered = await context.deliverInput(
              receipt,
              {
                ...body,
                binding_id: target.recipient.binding_id,
              },
              signal,
            )
          } else {
            definition ??= await context.publishDefinition(
              receipt,
              slackDefinition(route.kind),
              signal,
            )
            delivered = await context.deliverWorkflow(
              receipt,
              {
                ...body,
                route_id: target.configured.route_id,
                instance_key: route.providerRef,
                only_if_unbound: newlyMapped ? true : undefined,
                target: {
                  definition_id: definition.id,
                  provider_ref: route.providerRef,
                  provider_ref_kind: route.kind,
                  display_name: replay ? undefined : slackChannelDisplayName(event, route, labels),
                  parent_channel_id: target.parentChannelID,
                },
                grants: target.configured.grants,
              },
              signal,
            )
          }
          if (delivered.created_input)
            await applySlackInputEffects(
              client,
              event,
              delivered.canceled_interaction_ids ?? [],
              attempt,
            )
          completed.add(identity)
        } catch (error) {
          if (
            error instanceof ReceiptClientError &&
            error.apiCode === 'managed_work_admission_denied'
          ) {
            await sendSlackLaunchDenial(client, event, route, attempt)
            throw error
          }
          if (
            !(error instanceof ReceiptClientError) ||
            error.code !== 'http_error' ||
            (error.status !== 409 && !(target.kind === 'recipient' && error.status === 404)) ||
            render === 3
          )
            throw error
          conflict = error
          presentations.set(identity, presentation)
          retry = true
          break
        }
      }
      if (!retry) return
    }
  } finally {
    work?.release()
    clearTimeout(timer)
    controller.abort()
  }
}

type InputTarget =
  | { kind: 'recipient'; recipient: ChannelConnectorRecipient }
  | {
      kind: 'workflow'
      configured: SlackWorkflowRoute
      parentChannelID: string | undefined
      lookup: LookupChannelConnectorWorkflowResponse
    }

/** Discover even when no launch routes exist. Receive history suppresses launch;
 * outcomes from this receipt let a partially completed workflow fanout resume.
 * Read/send grants alone do not claim the conversation for a listener.
 */
async function* inputTargets(
  receipt: Readonly<ChannelConnectorEventReceipt>,
  context: SlackBehaviorContext,
  providerRef: string,
  keys: { own: string; sibling: string },
  signal: AbortSignal,
): AsyncGenerator<InputTarget> {
  const query: ReceiptRecipientsRequest = {
    provider_ref: providerRef,
    input_keys: [keys.own, keys.sibling],
  }
  let page = await context.lookupRecipients(receipt, query, signal)
  if (page.has_receive_binding_history && !page.workflow_started) {
    const cursors = new Set<string>()
    for (;;) {
      for (const recipient of page.recipients) yield { kind: 'recipient', recipient }
      if (page.next_cursor === null) return
      if (cursors.has(page.next_cursor)) throw new ReceiptClientError('invalid_response')
      cursors.add(page.next_cursor)
      page = await context.lookupRecipients(receipt, { ...query, cursor: page.next_cursor }, signal)
    }
  }
  const routes = await context.listRoutes(receipt, signal)
  for (const configured of routes) {
    const lookup = await context.lookupWorkflow(
      receipt,
      {
        route_id: configured.route_id,
        instance_key: providerRef,
        input_keys: query.input_keys,
      },
      signal,
    )
    yield { kind: 'workflow', configured, lookup, parentChannelID: page.parent_channel_id }
  }
}
