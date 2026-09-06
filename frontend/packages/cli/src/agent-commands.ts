import { openAgentEventStream } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import * as z from 'zod'

import { runChat } from './chat.tsx'
import { customOp, parseNumberFlag, parseWithSchema } from './factory.ts'
import { canPromptInteractively, promptAgentSelection } from './interactive.ts'
import { CliInputError } from './output.ts'

export const agentChatOp = customOp({
  verb: 'chat',
  summary: 'Chat interactively with an agent',
  path: schemas.zListAgentsPath,
  configure: (command) => {
    command.argument('[agent-id]')
  },
  run: async ({ client, path, args }) => {
    if (!canPromptInteractively()) {
      throw new CliInputError('agents chat needs an interactive terminal')
    }
    const agentID = parseWithSchema(
      schemas.zAgentId,
      args[0] ?? (await promptAgentSelection(client, path.orgID, path.projectID)),
      'agent id',
    )
    await runChat(client, { ...path, agentID })
  },
})

export const agentEventsStreamOp = customOp({
  verb: 'stream',
  summary: "Stream an agent's events as JSON lines",
  path: schemas.zListAgentsPath,
  configure: (command) => {
    command
      .argument('<agent-id>')
      .option('--after-sequence <sequence>', 'resume after this event sequence', parseNumberFlag)
      .option('--deltas', 'include best-effort model output preview frames')
  },
  run: async ({ client, path, args, options }) => {
    const agentID = parseWithSchema(schemas.zAgentId, args[0], 'agent id')
    const afterSequence =
      parseWithSchema(z.int().gte(0).optional(), options.afterSequence, '--after-sequence') ?? 0
    const abort = new AbortController()
    process.once('SIGINT', () => {
      abort.abort()
    })
    const frames = openAgentEventStream({
      client,
      path: { ...path, agentID },
      query: { after_sequence: afterSequence, stream_deltas: options.deltas === true },
      signal: abort.signal,
    })
    for await (const frame of frames) console.log(JSON.stringify(frame))
  },
})
