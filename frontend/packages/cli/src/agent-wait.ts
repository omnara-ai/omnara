import type { Agent, AgentActivity, GetAgentResponse, OmnaraClient } from '@omnara/sdk'
import { sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import * as z from 'zod'

import { customOp, parseNumberFlag, parseWithSchema } from './factory.ts'
import { formatRecord } from './format.ts'
import { CliInputError, renderResult, zOptionalRenderValue } from './output.ts'
import { pollUntilDeadline } from './poll.ts'

export const DEFAULT_AGENT_WAIT_TIMEOUT_SECONDS = 300
export const DEFAULT_AGENT_WAIT_STATE = 'idle' satisfies AgentActivity['state']

const zWaitState = schemas.zAgentActivity.shape.state

export const zAgentWaitStateFlags = z
  .union([z.string().transform((value) => [value]), z.array(z.string())])
  .optional()
  .transform((value) => value ?? [])

export const zAgentWaitTimeoutSeconds = z
  .number()
  .int()
  .gte(0)
  .optional()
  .transform((value) => value ?? DEFAULT_AGENT_WAIT_TIMEOUT_SECONDS)

export function collectRepeatableFlag(value: string, previous: string[] | undefined): string[] {
  return [...(previous ?? []), value]
}

export function parseAgentWaitStates(values: readonly string[]): AgentActivity['state'][] {
  const selected = values.length === 0 ? [DEFAULT_AGENT_WAIT_STATE] : values
  const states: AgentActivity['state'][] = []
  for (const value of selected) {
    const parsed = zWaitState.safeParse(value)
    if (!parsed.success) {
      throw new CliInputError(
        `invalid --state ${value}: expected running, idle, waiting_on_interaction, or archived`,
      )
    }
    if (!states.includes(parsed.data)) states.push(parsed.data)
  }
  return states
}

export function parseAgentWaitTimeoutSeconds(raw: number | undefined): number {
  return parseWithSchema(zAgentWaitTimeoutSeconds, raw, '--timeout')
}

export function agentWaitDeadline(nowMs: number, timeoutSeconds: number): string {
  if (timeoutSeconds === 0) {
    return new Date(nowMs + 100 * 365 * 24 * 60 * 60 * 1000).toISOString()
  }
  return new Date(nowMs + timeoutSeconds * 1000).toISOString()
}

export function agentMatchesWaitStates(
  agent: Pick<Agent, 'activity'>,
  states: readonly AgentActivity['state'][],
): boolean {
  const current = agent.activity?.state
  return current !== undefined && states.includes(current)
}

export function agentWaitExpiredMessage(
  states: readonly AgentActivity['state'][],
  timeoutSeconds: number,
): string {
  const wanted = states.join(', ')
  if (timeoutSeconds === 0) {
    return `agent did not reach activity state ${wanted}`
  }
  return `agent did not reach activity state ${wanted} within ${timeoutSeconds}s`
}

export async function waitForAgent(input: {
  client: OmnaraClient
  orgID: string
  projectID: string
  agentID: string
  states: readonly AgentActivity['state'][]
  timeoutSeconds: number
  nowMs?: number
}): Promise<GetAgentResponse> {
  return pollUntilDeadline({
    expiresAt: agentWaitDeadline(input.nowMs ?? Date.now(), input.timeoutSeconds),
    expiredMessage: agentWaitExpiredMessage(input.states, input.timeoutSeconds),
    async fetchOnce() {
      const { data } = await sdk.getAgent({
        client: input.client,
        path: { orgID: input.orgID, projectID: input.projectID, agentID: input.agentID },
      })
      if (agentMatchesWaitStates(data.agent, input.states)) return data
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
        '--state <state>',
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
      parseWithSchema(zAgentWaitStateFlags, options.state, '--state'),
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
    const formatted = formatRecord()(response)
    renderResult(zOptionalRenderValue.parse(formatted.value), options.json === true)
  },
})
