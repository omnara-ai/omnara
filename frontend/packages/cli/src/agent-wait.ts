import { isAgentActive } from '@omnara/react'
import type { Agent, AgentActivity, GetAgentResponse, OmnaraClient } from '@omnara/sdk'
import { sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import * as z from 'zod'

import { customOp, parseNumberFlag, parseWithSchema } from './factory.ts'
import { CliInputError, renderResult, zOptionalRenderValue } from './output.ts'
import { PollTerminalError, pollUntilDeadline } from './poll.ts'

const DEFAULT_AGENT_WAIT_TIMEOUT_SECONDS = 300
const DEFAULT_AGENT_WAIT_STATE = 'idle' satisfies AgentActivity['state']

const zWaitState = schemas.zAgentActivity.shape.state

const zAgentWaitStateFlags = z
  .array(z.string())
  .optional()
  .transform((value) => value ?? [])

const zAgentWaitTimeoutSeconds = z
  .number()
  .int()
  .gte(0)
  .optional()
  .transform((value) => value ?? DEFAULT_AGENT_WAIT_TIMEOUT_SECONDS)

function collectRepeatableFlag(value: string, previous: string[] | undefined): string[] {
  return [...(previous ?? []), value]
}

function parseAgentWaitStates(values: readonly string[]): AgentActivity['state'][] {
  const selected = values.length === 0 ? [DEFAULT_AGENT_WAIT_STATE] : values
  const states: AgentActivity['state'][] = []
  for (const value of selected) {
    const parsed = zWaitState.safeParse(value)
    if (!parsed.success) {
      throw new CliInputError(
        `invalid --activity-state ${value}: expected running, idle, waiting_on_interaction, or archived`,
      )
    }
    if (!states.includes(parsed.data)) states.push(parsed.data)
  }
  return states
}

function agentWaitDeadline(nowMs: number, timeoutSeconds: number): string {
  if (timeoutSeconds === 0) {
    return new Date(nowMs + 100 * 365 * 24 * 60 * 60 * 1000).toISOString()
  }
  return new Date(nowMs + timeoutSeconds * 1000).toISOString()
}

function agentMatchesWaitStates(agent: Agent, states: readonly AgentActivity['state'][]): boolean {
  const current = agent.activity?.state
  if (current === undefined || !states.includes(current)) return false
  // An idle agent with working subagents or background processes can wake again, so it is not done yet.
  return current !== 'idle' || !isAgentActive(agent)
}

function agentWaitExpiredMessage(
  states: readonly AgentActivity['state'][],
  timeoutSeconds: number,
): string {
  const wanted = states.join(', ')
  if (timeoutSeconds === 0) {
    return `agent did not reach activity state ${wanted}`
  }
  return `agent did not reach activity state ${wanted} within ${timeoutSeconds}s`
}

function agentArchivedMessage(states: readonly AgentActivity['state'][]): string {
  return `agent was archived before reaching activity state ${states.join(', ')}`
}

async function waitForAgent(input: {
  client: OmnaraClient
  orgID: string
  projectID: string
  agentID: string
  states: readonly AgentActivity['state'][]
  timeoutSeconds: number
}): Promise<GetAgentResponse> {
  return pollUntilDeadline({
    expiresAt: agentWaitDeadline(Date.now(), input.timeoutSeconds),
    expiredMessage: agentWaitExpiredMessage(input.states, input.timeoutSeconds),
    async fetchOnce(signal) {
      const { data } = await sdk.getAgent({
        client: input.client,
        path: { orgID: input.orgID, projectID: input.projectID, agentID: input.agentID },
        signal,
      })
      if (agentMatchesWaitStates(data.agent, input.states)) return data
      // Archiving is terminal, so an archived agent can never reach another state.
      const archived = data.agent.state === 'archived' || data.agent.activity?.state === 'archived'
      if (archived) throw new PollTerminalError(agentArchivedMessage(input.states))
      return undefined
    },
  })
}

export const agentWaitOp = customOp({
  verb: 'wait',
  summary: 'Wait until an agent reaches an activity state',
  path: schemas.zListAgentsPath,
  configure: (command) => {
    command
      .argument('<agent-id>')
      .option(
        '--activity-state <state>',
        'activity state to wait for; repeatable (default: idle)',
        collectRepeatableFlag,
      )
      .option(
        '--timeout <seconds>',
        'seconds to wait before failing; 0 waits until interrupted (default: 300)',
        parseNumberFlag,
      )
      .option('--json', 'print the raw JSON response')
  },
  run: async ({ client, path, args, options }) => {
    const agentID = parseWithSchema(schemas.zAgentId, args[0], 'agent id')
    const states = parseAgentWaitStates(
      parseWithSchema(zAgentWaitStateFlags, options.activityState, '--activity-state'),
    )
    const timeoutSeconds = parseWithSchema(zAgentWaitTimeoutSeconds, options.timeout, '--timeout')
    const response = await waitForAgent({
      client,
      orgID: path.orgID,
      projectID: path.projectID,
      agentID,
      states,
      timeoutSeconds,
    })
    renderResult(zOptionalRenderValue.parse(response), options.json === true)
  },
})
