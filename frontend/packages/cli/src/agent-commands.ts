import type { AgentEvent, ListAgentEventsResponse } from '@omnara/sdk'
import { AgentEventStreamError, openAgentEventStream, sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import { Option } from 'commander'
import * as z from 'zod'

import { runChat } from './chat.ts'
import {
  type CustomSpec,
  parseWithSchema,
  planPathParams,
  registerPathParams,
  resolvePathValues,
} from './factory.ts'
import type { OutputFormat } from './format.ts'
import { canPromptInteractively, promptAgentSelection } from './interactive.ts'
import { CliInputError, renderResult, runCliAction } from './output.ts'

const zProjectScopePath = z.object({
  orgID: schemas.zOrganizationId,
  projectID: schemas.zProjectId,
})

export const zListEventsCliQuery = z.object({
  before_sequence: z.int().gte(0).optional(),
  after_sequence: z.int().gte(0).optional(),
  limit: z.int().gte(1).lte(500).optional(),
})

function abbreviate(text: string, max = 80): string {
  const flat = text.replaceAll(/\s+/g, ' ').trim()
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat
}

function eventPreview(event: AgentEvent): string {
  switch (event.event_kind) {
    case 'agent_input':
      return abbreviate(
        event.content_blocks
          .map((block) => (block.type === 'text' ? block.text : `[${block.type}]`))
          .join(' '),
      )
    case 'model_output':
      return abbreviate(
        event.content_blocks
          .map((block) =>
            block.type === 'text'
              ? block.text
              : block.type === 'tool_call'
                ? `[tool_call ${block.name}]`
                : `[${block.type}]`,
          )
          .join(' '),
      )
    case 'tool_result': {
      const output = event.content_blocks
        .map((block) =>
          block.type === 'text'
            ? block.text
            : block.type === 'structured_data'
              ? JSON.stringify(block.value)
              : `[${block.type}]`,
        )
        .join(' ')
      return abbreviate(`${event.outcome}: ${output}`)
    }
    case 'context_checkpoint':
      return abbreviate(`context summarized: ${event.summary}`)
  }
}

export const formatAgentEventList: OutputFormat<ListAgentEventsResponse> = (response) => ({
  value: {
    data: response.data.map((event) => ({
      sequence: event.sequence,
      event_kind: event.event_kind,
      turn_sequence: event.turn_sequence,
      preview: eventPreview(event),
      created_at: event.created_at,
    })),
    has_more: response.has_more,
    ...(response.next_before_sequence == null
      ? {}
      : { next_before_sequence: response.next_before_sequence }),
  },
})

export const agentChatOp: CustomSpec = {
  type: 'custom',
  register(parent, config) {
    const command = parent
      .command('chat')
      .description('Chat interactively with an agent')
      .argument('[agent-id]')
    const plan = planPathParams(zProjectScopePath, [])
    registerPathParams(command, plan)
    command.action(async (agentArg: string | undefined) => {
      await runCliAction(async () => {
        if (!canPromptInteractively()) {
          throw new CliInputError('agents chat needs an interactive terminal')
        }
        const options = command.opts<Record<string, unknown>>()
        const scope = parseWithSchema(
          zProjectScopePath,
          await resolvePathValues(plan, [], options, config),
          'arguments',
        )
        const agentId = parseWithSchema(
          schemas.zAgentId,
          agentArg ?? (await promptAgentSelection(config.client, scope.orgID, scope.projectID)),
          'agent id',
        )
        await runChat({
          client: config.client,
          orgId: scope.orgID,
          projectId: scope.projectID,
          agentId,
        })
      })
    })
  },
}

const zInputContentBlocks = z.array(schemas.zCreateAgentInputContentBlock).min(1)

const zContentBlocksFlag = z
  .string()
  .transform((raw, ctx): unknown => {
    try {
      return JSON.parse(raw)
    } catch (error) {
      ctx.addIssue(error instanceof Error ? error.message : String(error))
      return z.NEVER
    }
  })
  .pipe(zInputContentBlocks)

export const agentInputOp: CustomSpec = {
  type: 'custom',
  register(parent, config) {
    const command = parent
      .command('input')
      .description('Send input to an agent')
      .argument('<agent-id>')
      .argument('[message]', 'plain text to send as a single text block')
      .option('--blocks <json>', 'content blocks as a JSON array, instead of a plain message')
      .addOption(
        new Option('--delivery-mode <mode>', 'how the input is delivered').choices([
          'queued',
          'steering',
        ]),
      )
      .option('--cancel-open-interactions', 'cancel open interactions when the input lands')
      .option('--json', 'print the raw JSON response')
    const plan = planPathParams(zProjectScopePath, [])
    registerPathParams(command, plan)
    command.action(async (agentArg: string, message: string | undefined) => {
      await runCliAction(async () => {
        const options = command.opts<Record<string, unknown>>()
        const scope = parseWithSchema(
          zProjectScopePath,
          await resolvePathValues(plan, [], options, config),
          'arguments',
        )
        const agentID = parseWithSchema(schemas.zAgentId, agentArg, 'agent id')
        if ((message === undefined) === (options.blocks === undefined)) {
          throw new CliInputError('pass either a message argument or --blocks, not both')
        }
        const contentBlocks =
          message === undefined
            ? parseWithSchema(zContentBlocksFlag, options.blocks, '--blocks')
            : parseWithSchema(zInputContentBlocks, [{ type: 'text', text: message }], 'message')
        const body = parseWithSchema(
          schemas.zCreateAgentInputBody,
          {
            content_blocks: contentBlocks,
            ...(options.deliveryMode === undefined ? {} : { delivery_mode: options.deliveryMode }),
            ...(options.cancelOpenInteractions === true
              ? { cancel_open_interactions: true }
              : {}),
          },
          'request body',
        )
        const { data } = await sdk.createAgentInput({
          client: config.client,
          path: { ...scope, agentID },
          body,
        })
        renderResult(options.json === true ? data : data.agent_input, options.json === true)
      })
    })
  },
}

export const agentEventsStreamOp: CustomSpec = {
  type: 'custom',
  register(parent, config) {
    const command = parent
      .command('stream')
      .description("Stream an agent's events as JSON lines")
      .argument('<agent-id>')
      .option('--after-sequence <sequence>', 'resume after this event sequence')
      .option('--deltas', 'include best-effort model output preview frames')
    const plan = planPathParams(zProjectScopePath, [])
    registerPathParams(command, plan)
    command.action(async (agentArg: string) => {
      await runCliAction(async () => {
        const options = command.opts<Record<string, unknown>>()
        const scope = parseWithSchema(
          zProjectScopePath,
          await resolvePathValues(plan, [], options, config),
          'arguments',
        )
        const agentID = parseWithSchema(schemas.zAgentId, agentArg, 'agent id')
        let afterSequence =
          parseWithSchema(
            z.coerce.number().int().gte(0).optional(),
            options.afterSequence,
            '--after-sequence',
          ) ?? 0
        const abort = new AbortController()
        process.once('SIGINT', () => {
          abort.abort()
        })
        for (;;) {
          try {
            const { stream } = await openAgentEventStream({
              client: config.client,
              path: { ...scope, agentID },
              query: { after_sequence: afterSequence, stream_deltas: options.deltas === true },
              signal: abort.signal,
            })
            for await (const frame of stream) {
              if ('event_kind' in frame) afterSequence = Math.max(afterSequence, frame.sequence)
              console.log(JSON.stringify(frame))
            }
          } catch (error) {
            if (abort.signal.aborted) return
            if (error instanceof AgentEventStreamError && error.retryable) {
              await new Promise((resolve) => setTimeout(resolve, 1000))
              continue
            }
            throw error
          }
        }
      })
    })
  },
}
