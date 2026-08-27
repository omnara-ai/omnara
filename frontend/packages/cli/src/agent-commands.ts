import type { AgentEvent, ListAgentEventsResponse } from '@omnara/sdk'
import { sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import type { Command } from 'commander'
import * as z from 'zod'

import { followAgentEvents } from './agent-events.ts'
import { runChat } from './chat.ts'
import type { CliConfig } from './config.ts'
import { blockText } from './content-blocks.ts'
import {
  type CustomSpec,
  op,
  parseNumberFlag,
  parseWithSchema,
  planPathParams,
  registerPathParams,
  resolvePathValues,
} from './factory.ts'
import { formatRecord, type OutputFormat } from './format.ts'
import { canPromptInteractively, promptAgentSelection } from './interactive.ts'
import { abbreviate, CliInputError, runCliAction } from './output.ts'

const zProjectScopePath = schemas.zListAgentsPath

export const zListEventsCliQuery = z.object({
  before_sequence: z.int().gte(0).optional(),
  after_sequence: z.int().gte(0).optional(),
  limit: z.int().gte(1).lte(500).optional(),
})

const previewWidth = 80

function eventPreview(event: AgentEvent): string {
  switch (event.event_kind) {
    case 'agent_input':
    case 'model_output':
      return abbreviate(event.content_blocks.map(blockText).join(' '), previewWidth)
    case 'tool_result':
      return abbreviate(
        `${event.outcome}: ${event.content_blocks.map(blockText).join(' ')}`,
        previewWidth,
      )
    case 'context_checkpoint':
      return abbreviate(`context summarized: ${event.summary}`, previewWidth)
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
    next_after_sequence: response.next_after_sequence,
    ...(response.next_before_sequence == null
      ? {}
      : { next_before_sequence: response.next_before_sequence }),
  },
})

function registerProjectScope(
  command: Command,
  config: CliConfig,
): () => Promise<z.output<typeof zProjectScopePath>> {
  const plan = planPathParams(zProjectScopePath, [])
  registerPathParams(command, plan)
  return async () =>
    parseWithSchema(
      zProjectScopePath,
      await resolvePathValues(plan, [], command.opts<Record<string, unknown>>(), config),
      'arguments',
    )
}

export const agentChatOp: CustomSpec = {
  type: 'custom',
  register(parent, config) {
    const command = parent
      .command('chat')
      .description('Chat interactively with an agent')
      .argument('[agent-id]')
    const resolveScope = registerProjectScope(command, config)
    command.action(async (agentArg: string | undefined) => {
      await runCliAction(async () => {
        if (!canPromptInteractively()) {
          throw new CliInputError('agents chat needs an interactive terminal')
        }
        const scope = await resolveScope()
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

const zAgentInputCliBody = schemas.zCreateAgentInputBody
  .extend({
    content_blocks: z
      .array(schemas.zCreateAgentInputContentBlock)
      .min(1)
      .optional()
      .describe('content blocks as a JSON array, instead of --message'),
    message: z.string().optional().describe('plain text to send as a single text block'),
  })
  .transform(({ message, content_blocks, ...rest }, ctx) => {
    if (message !== undefined && content_blocks !== undefined) {
      ctx.addIssue('pass either --message or --content-blocks, not both')
      return z.NEVER
    }
    if (content_blocks !== undefined) return { ...rest, content_blocks }
    if (message !== undefined)
      return { ...rest, content_blocks: [{ type: 'text' as const, text: message }] }
    ctx.addIssue('pass --message or --content-blocks')
    return z.NEVER
  })

export const agentInputOp = op({
  verb: 'input',
  summary: 'Send input to an agent',
  fn: sdk.createAgentInput,
  format: (response) => formatRecord()(response.agent_input),
  path: schemas.zCreateAgentInputPath,
  body: zAgentInputCliBody,
})

export const agentEventsStreamOp: CustomSpec = {
  type: 'custom',
  register(parent, config) {
    const command = parent
      .command('stream')
      .description("Stream an agent's events as JSON lines")
      .argument('<agent-id>')
      .option('--after-sequence <sequence>', 'resume after this event sequence', parseNumberFlag)
      .option('--deltas', 'include best-effort model output preview frames')
    const resolveScope = registerProjectScope(command, config)
    command.action(async (agentArg: string) => {
      await runCliAction(async () => {
        const options = command.opts<Record<string, unknown>>()
        const scope = await resolveScope()
        const agentID = parseWithSchema(schemas.zAgentId, agentArg, 'agent id')
        const afterSequence =
          parseWithSchema(z.int().gte(0).optional(), options.afterSequence, '--after-sequence') ?? 0
        const abort = new AbortController()
        process.once('SIGINT', () => {
          abort.abort()
        })
        const frames = followAgentEvents({
          client: config.client,
          path: { ...scope, agentID },
          afterSequence,
          streamDeltas: options.deltas === true,
          signal: abort.signal,
        })
        for await (const frame of frames) console.log(JSON.stringify(frame))
      })
    })
  },
}
