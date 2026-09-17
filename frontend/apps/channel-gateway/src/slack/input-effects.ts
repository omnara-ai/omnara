import { schemas } from '@omnara/sdk'
import { z } from 'zod'

import type { OperationAttemptContext } from '../operations/retry'
import { type SlackClient } from './client'
import { SlackAPIError } from './errors'
import type { SlackInboundEvent, SlackInboundRoute } from './events'
import { promptText } from './prompts'
import type { SlackMessage } from './protocol'

const actionSchema = z.object({ action_id: z.string(), value: z.string().optional() })
const blockSchema = z.object({
  block_id: z.string().optional(),
  element: actionSchema.optional(),
  elements: z.array(actionSchema).optional(),
})
const valueSchema = z.object({
  type: z.literal('omnara_interaction'),
  interaction_id: schemas.zAgentInteractionId,
  agent_id: schemas.zAgentId,
  integration_target_id: schemas.zIntegrationTargetId,
})

/** Only the typed, definite core admission denial permits this notice. A failed
 * cosmetic publication does not schedule another attempt or retry the input.
 */
export async function sendSlackLaunchDenial(
  client: SlackClient,
  event: SlackInboundEvent,
  route: SlackInboundRoute,
  context: OperationAttemptContext,
): Promise<void> {
  if (context.signal.aborted || context.deadlineMs <= Date.now()) return
  const payload = {
    channel: event.channel,
    text: "I couldn't complete this request. Please try again later or contact this bot's owner.",
    thread_ts: route.kind === 'thread' ? (event.thread_ts ?? event.ts) : undefined,
  }
  try {
    await client.api('chat.postMessage', payload, {
      ...context,
      deadlineMs: Math.min(context.deadlineMs, Date.now() + 1500),
    })
  } catch {
    /* The original definite admission denial remains the receipt outcome. */
  }
}

/** Native post-admission UI effects. Each request happens at most once in this
 * invocation; a failure never retries the accepted input or changes its outcome.
 * An already_reacted response is successful replay.
 */
export async function applySlackInputEffects(
  client: SlackClient,
  event: SlackInboundEvent,
  canceledInteractionIDs: readonly string[],
  context: OperationAttemptContext,
): Promise<void> {
  if (context.signal.aborted || context.deadlineMs <= Date.now()) return
  const controller = new AbortController()
  const deadlineMs = Math.min(context.deadlineMs, Date.now() + 1500)
  const timer = setTimeout(
    () => {
      controller.abort()
    },
    Math.max(0, deadlineMs - Date.now()),
  )
  const attempt = {
    ...context,
    deadlineMs,
    signal: AbortSignal.any([context.signal, controller.signal]),
  }
  try {
    await Promise.allSettled([
      addReaction(client, event, attempt),
      dismissPrompts(client, event, canceledInteractionIDs, attempt),
    ])
  } finally {
    clearTimeout(timer)
    controller.abort()
  }
}

async function addReaction(
  client: SlackClient,
  event: SlackInboundEvent,
  context: OperationAttemptContext,
) {
  try {
    await client.api(
      'reactions.add',
      { channel: event.channel, timestamp: event.ts, name: 'eyes' },
      context,
    )
  } catch (cause) {
    if (cause instanceof SlackAPIError && cause.code === 'already_reacted') return
    throw cause
  }
}

async function dismissPrompts(
  client: SlackClient,
  event: SlackInboundEvent,
  ids: readonly string[],
  context: OperationAttemptContext,
) {
  if (!ids.length) return
  const canceled = new Set(ids)
  const args = { channel: event.channel, latest: event.ts, inclusive: false, limit: 15 }
  const page =
    event.thread_ts && event.thread_ts !== event.ts
      ? await client.api('conversations.replies', { ...args, ts: event.thread_ts }, context)
      : await client.api('conversations.history', args, context)
  for (const message of page.messages) {
    if (!isCanceledPrompt(message, canceled)) continue
    const label = promptText(message.text, 3000)
    const dismissed = 'Dismissed because a newer message was sent.'
    try {
      await client.api(
        'chat.update',
        {
          channel: event.channel,
          ts: message.ts,
          as_user: true,
          text: promptText(`${label}\n${dismissed}`, 3000),
          blocks: [
            { type: 'section', text: { type: 'plain_text', text: label } },
            { type: 'context', elements: [{ type: 'plain_text', text: dismissed }] },
          ],
        },
        context,
      )
    } catch (cause) {
      // Match native cleanup: a deleted copy cannot prevent updating other copies.
      if (!(cause instanceof SlackAPIError) || cause.code !== 'message_not_found') throw cause
    }
  }
}

function isCanceledPrompt(message: SlackMessage, ids: ReadonlySet<string>): boolean {
  for (const raw of message.blocks ?? []) {
    const parsed = blockSchema.safeParse(raw)
    if (!parsed.success) continue
    const block = parsed.data
    for (const id of ids) if (block.block_id === `omnara_interaction_${id}`) return true
    const actions = [...(block.elements ?? [])]
    if (block.element) actions.push(block.element)
    for (const action of actions) {
      if (
        (action.action_id !== 'omnara_interaction' &&
          !action.action_id.startsWith('omnara_interaction_')) ||
        !action.value
      )
        continue
      try {
        const value = valueSchema.safeParse(JSON.parse(action.value))
        if (value.success && ids.has(value.data.interaction_id)) return true
      } catch {
        /* Unrelated malformed provider actions are not our prompt copies. */
      }
    }
  }
  return false
}
