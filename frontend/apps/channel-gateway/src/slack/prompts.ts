import type {
  ChannelInteractionOperation,
  ChannelInteractionOperationResult,
  InteractionForm,
} from '@omnara/sdk'

import { type OperationRetryOptions, retryOperation } from '../operations/retry'
import { SlackAPIError, type SlackClient } from './client'
import { slackDestination } from './operations'
import type { SlackRequest } from './protocol'

type SlackPrompt = Extract<SlackRequest['chat.postMessage'], { blocks: unknown }> & { text: string }
type Blocks = SlackPrompt['blocks']

/** Preserve native prompt.go's existing labels, action values and atomic submit.
 * The form and IDs are canonical core data; rendering never resolves an interaction.
 */
export function renderSlackInteraction(input: ChannelInteractionOperation): SlackPrompt {
  const target = slackDestination(input.destination)
  const form = input.form
  const heading = [
    form.title,
    ...(form.context ?? []).map((item) => `${item.label}: ${item.value}`),
  ].join('\n')
  const summary = [
    heading,
    ...form.questions.flatMap((question, index) => [
      `${index + 1}. ${question.prompt}`,
      ...question.options.map((option, optionIndex) => `   ${optionIndex + 1}. ${option.label}`),
    ]),
  ].join('\n')
  const supported =
    form.questions.length + 2 <= 50 &&
    form.questions.every((question) => question.options.length <= 10)
  const blocks: Blocks = [
    {
      type: 'section',
      block_id: `omnara_interaction_${input.interaction_id}`,
      text: { type: 'plain_text', text: promptText(supported ? heading : summary, 3000) },
    },
  ]
  if (supported) {
    blocks.push(...questionBlocks(form), {
      type: 'actions',
      elements: [
        {
          type: 'button',
          text: { type: 'plain_text', text: 'Submit' },
          action_id: 'omnara_interaction',
          value: JSON.stringify({
            type: 'omnara_interaction',
            interaction_id: input.interaction_id,
            agent_id: input.agent_id,
            // Existing Slack callback wire name. Always use the pinned canonical ID.
            integration_target_id: input.channel_id,
          }),
          style: 'primary',
        },
      ],
    })
  } else {
    blocks.push({
      type: 'section',
      text: { type: 'plain_text', text: 'Respond in Omnara to continue.' },
    })
  }
  const request: SlackPrompt = {
    channel: target.channel,
    text: promptText(summary, 3000),
    blocks,
  }
  if (target.threadTs) request.thread_ts = target.threadTs
  return request
}

export function sendSlackInteraction(
  client: SlackClient,
  input: ChannelInteractionOperation,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<ChannelInteractionOperationResult> {
  const request = renderSlackInteraction(input)
  return retryOperation(operation, async (context) => {
    const result = await client.api('chat.postMessage', request, context)
    if (
      (result.channel !== undefined && result.channel !== request.channel) ||
      result.response_metadata?.warnings?.includes('message_truncated')
    ) {
      throw new SlackAPIError('invalid_publication_response', { outcomeUnknown: true })
    }
    return { message_id: result.ts }
  })
}

function questionBlocks(form: InteractionForm): Blocks {
  return form.questions.map((question, index) => ({
    type: 'input',
    block_id: `omnara_question_${index}`,
    element: {
      type: question.multiple ? 'checkboxes' : 'radio_buttons',
      action_id: 'omnara_answer',
      options: question.options.map((option, optionIndex) => ({
        text: { type: 'plain_text', text: promptText(option.label, 75) },
        value: String(optionIndex),
      })),
    },
    label: { type: 'plain_text', text: promptText(question.prompt, 2000) },
  }))
}

export function promptText(value: string, limit: number): string {
  const text = value.trim()
  const runes = Array.from(text)
  return runes.length <= limit ? text : `${runes.slice(0, limit - 3).join('')}...`
}
