import { openAgentEventStream } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import type { Command } from 'commander'
import * as z from 'zod'

import { runChat } from './chat.ts'
import type { CliConfig } from './config.ts'
import {
  type CustomSpec,
  parseNumberFlag,
  parseWithSchema,
  planPathParams,
  registerPathParams,
  resolvePathValues,
} from './factory.ts'
import { canPromptInteractively, promptAgentSelection } from './interactive.ts'
import { CliInputError, runCliAction } from './output.ts'

const zProjectScopePath = schemas.zListAgentsPath

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

export const zAgentInputCliBody = schemas.zCreateAgentInputBody
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
        const frames = openAgentEventStream({
          client: config.client,
          path: { ...scope, agentID },
          query: { after_sequence: afterSequence, stream_deltas: options.deltas === true },
          signal: abort.signal,
        })
        for await (const frame of frames) console.log(JSON.stringify(frame))
      })
    })
  },
}
