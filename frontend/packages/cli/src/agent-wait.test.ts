import { createOmnaraClient, type JsonBody } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  agentMatchesWaitStates,
  agentWaitDeadline,
  agentWaitExpiredMessage,
  collectRepeatableFlag,
  DEFAULT_AGENT_WAIT_TIMEOUT_SECONDS,
  parseAgentWaitStates,
  parseAgentWaitTimeoutSeconds,
  waitForAgent,
} from './agent-wait.ts'
import { CliInputError } from './output.ts'

const id = (prefix: string) => `${prefix}_${'a'.repeat(26)}`
const now = '2026-10-14T00:00:00Z'
const path = { orgID: id('org'), projectID: id('proj'), agentID: id('agt') }

function json(value: JsonBody, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function agentPayload(state: 'running' | 'idle' | 'waiting_on_interaction' | 'archived') {
  return {
    agent: {
      id: path.agentID,
      org_id: path.orgID,
      project_id: path.projectID,
      state: state === 'archived' ? 'archived' : 'active',
      name: 'wait-demo',
      created_at: now,
      updated_at: now,
      activity: { state, last_activity_at: now },
    },
    machine_ids: [],
    mcp_connections: [],
  }
}

describe('parseAgentWaitStates', () => {
  it('defaults to idle when no --state flags are given', () => {
    expect(parseAgentWaitStates([])).toEqual(['idle'])
  })

  it('accepts a single state or a repeatable list without duplicates', () => {
    expect(parseAgentWaitStates(['running'])).toEqual(['running'])
    expect(parseAgentWaitStates(['idle', 'archived', 'idle'])).toEqual(['idle', 'archived'])
  })

  it('rejects an unknown activity state', () => {
    expect(() => parseAgentWaitStates(['nope'])).toThrow(CliInputError)
    expect(() => parseAgentWaitStates(['nope'])).toThrow(/invalid --state nope/)
  })
})

describe('parseAgentWaitTimeoutSeconds', () => {
  it('defaults to 300 seconds and accepts zero', () => {
    expect(parseAgentWaitTimeoutSeconds(undefined)).toBe(DEFAULT_AGENT_WAIT_TIMEOUT_SECONDS)
    expect(parseAgentWaitTimeoutSeconds(0)).toBe(0)
  })

  it('rejects a negative timeout', () => {
    expect(() => parseAgentWaitTimeoutSeconds(-1)).toThrow(CliInputError)
  })
})

describe('agent wait helpers', () => {
  it('collects repeatable flag values', () => {
    expect(collectRepeatableFlag('idle', undefined)).toEqual(['idle'])
    expect(collectRepeatableFlag('archived', ['idle'])).toEqual(['idle', 'archived'])
  })

  it('matches only listed activity states', () => {
    expect(
      agentMatchesWaitStates({ activity: { state: 'idle', last_activity_at: now } }, ['idle']),
    ).toBe(true)
    expect(
      agentMatchesWaitStates({ activity: { state: 'running', last_activity_at: now } }, [
        'idle',
        'archived',
      ]),
    ).toBe(false)
    expect(agentMatchesWaitStates({}, ['idle'])).toBe(false)
  })

  it('describes the wait deadline', () => {
    expect(agentWaitDeadline(Date.parse('2026-01-01T00:00:00Z'), 5)).toBe(
      '2026-01-01T00:00:05.000Z',
    )
    expect(agentWaitExpiredMessage(['idle'], 30)).toBe(
      'agent did not reach activity state idle within 30s',
    )
    expect(agentWaitExpiredMessage(['idle', 'archived'], 0)).toBe(
      'agent did not reach activity state idle, archived',
    )
  })
})

describe('waitForAgent', () => {
  it('returns immediately when the agent is already in the requested state', async () => {
    let fetches = 0
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: () => {
        fetches += 1
        return Promise.resolve(json(agentPayload('idle')))
      },
    })
    const result = await waitForAgent({
      client,
      ...path,
      states: ['idle'],
      timeoutSeconds: 30,
    })
    expect(fetches).toBe(1)
    expect(result.agent.activity?.state).toBe('idle')
    expect(result.agent.id).toBe(path.agentID)
  })

  it('fails when the timeout elapses before the activity matches', async () => {
    let fetches = 0
    const client = createOmnaraClient({
      baseUrl: 'https://omnara.test/api/v1',
      fetch: () => {
        fetches += 1
        return Promise.resolve(json(agentPayload('running')))
      },
    })
    await expect(
      waitForAgent({
        client,
        ...path,
        states: ['idle'],
        timeoutSeconds: 1,
        nowMs: Date.now() - 5_000,
      }),
    ).rejects.toThrow(/did not reach activity state idle within 1s/)
    expect(fetches).toBe(1)
  })
})
