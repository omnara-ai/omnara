import { type AgentActivity, createOmnaraClient } from '@omnara/sdk'
import { Command } from 'commander'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { registerGroup } from './factory.ts'
import { commandGroups } from './manifest.ts'

const id = (prefix: string) => `${prefix}_${'a'.repeat(26)}`
const now = '2026-10-14T00:00:00Z'
const orgID = id('org')
const projectID = id('proj')
const agentID = id('agt')
const apiUrl = 'https://omnara.example/api/v1'
const agentUrl = `${apiUrl}/orgs/${orgID}/projects/${projectID}/agents/${agentID}`
const originalExitCode = process.exitCode

beforeEach(() => {
  vi.spyOn(console, 'log').mockImplementation(() => undefined)
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  process.exitCode = originalExitCode
  vi.useRealTimers()
  vi.restoreAllMocks()
})

function activity(state: AgentActivity['state']): AgentActivity {
  return {
    state,
    last_activity_at: now,
    open_interactions: { question_count: 0, permission_request_count: 0 },
    running_processes: { count: 0, process_ids: [] },
    working_subagents: { count: 0, agent_ids: [] },
  }
}

function agentPayload(state: AgentActivity['state']) {
  return {
    agent: {
      id: agentID,
      org_id: orgID,
      project_id: projectID,
      state: state === 'archived' ? 'archived' : 'active',
      name: 'wait-demo',
      created_at: now,
      updated_at: now,
      activity: activity(state),
    },
    machine_ids: [],
    mcp_connections: [],
  }
}

function cli(reply: (request: Request) => Response | Promise<Response>) {
  const requests: Request[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = new Request(input, init)
    requests.push(request)
    return reply(request)
  }
  const client = createOmnaraClient({ baseUrl: apiUrl, fetch })
  return {
    requests,
    run: async (...args: string[]) => {
      const group = commandGroups.find((group) => group.name === 'agents')
      if (!group) throw new Error('agents command is missing')
      const program = new Command().exitOverride()
      registerGroup(
        program,
        {
          client,
          apiUrl,
          issuerUrl: 'https://omnara.example',
          defaultOrgId: orgID,
          defaultProjectId: projectID,
          store: { path: '', read: () => ({}), update: () => ({}) },
          fetch,
          sleep: () => Promise.resolve(),
          ensureLoggedIn: () => Promise.resolve(),
        },
        group,
      )
      await program.parseAsync(['agents', 'wait', agentID, ...args], { from: 'user' })
    },
  }
}

function printed(state: AgentActivity['state']): string {
  return JSON.stringify(agentPayload(state), null, 2)
}

describe('agents wait', () => {
  it('waits for idle by default and prints the agent', async () => {
    const command = cli(() => Response.json(agentPayload('idle')))
    await command.run('--json')
    expect(command.requests).toHaveLength(1)
    expect(command.requests[0]?.url).toBe(agentUrl)
    expect(console.log).toHaveBeenCalledWith(printed('idle'))
    expect(console.error).not.toHaveBeenCalled()
  })

  it('keeps polling until a requested state matches', async () => {
    const states: AgentActivity['state'][] = ['running', 'waiting_on_interaction']
    const command = cli(() => Response.json(agentPayload(states.shift() ?? 'idle')))
    vi.useFakeTimers()
    const run = command.run(
      '--activity-state',
      'waiting_on_interaction',
      '--activity-state',
      'idle',
      '--json',
    )
    await vi.advanceTimersByTimeAsync(2_000)
    await run
    expect(command.requests).toHaveLength(2)
    expect(console.log).toHaveBeenCalledWith(printed('waiting_on_interaction'))
    expect(console.error).not.toHaveBeenCalled()
  })

  it('keeps waiting while an idle agent still has background work', async () => {
    const busy = agentPayload('idle')
    busy.agent.activity.working_subagents = { count: 1, agent_ids: [id('agt')] }
    const replies = [busy, agentPayload('idle')]
    const command = cli(() => Response.json(replies.shift() ?? agentPayload('idle')))
    vi.useFakeTimers()
    const run = command.run('--json')
    await vi.advanceTimersByTimeAsync(2_000)
    await run
    expect(command.requests).toHaveLength(2)
    expect(console.log).toHaveBeenCalledWith(printed('idle'))
    expect(console.error).not.toHaveBeenCalled()
  })

  it('rejects an unknown state before calling the API', async () => {
    const command = cli(() => Response.json(agentPayload('idle')))
    await command.run('--activity-state', 'nope')
    expect(command.requests).toHaveLength(0)
    expect(process.exitCode).toBe(1)
    expect(console.error).toHaveBeenCalledWith(
      expect.stringContaining('invalid --activity-state nope'),
    )
  })

  it('rejects a negative timeout before calling the API', async () => {
    const command = cli(() => Response.json(agentPayload('idle')))
    await command.run('--timeout', '-1')
    expect(command.requests).toHaveLength(0)
    expect(process.exitCode).toBe(1)
    expect(console.error).toHaveBeenCalledWith(expect.stringContaining('--timeout'))
  })

  it('fails at once when the agent is archived and archived was not requested', async () => {
    const command = cli(() => Response.json(agentPayload('archived')))
    await command.run('--timeout', '0')
    expect(command.requests).toHaveLength(1)
    expect(process.exitCode).toBe(1)
    expect(console.error).toHaveBeenCalledWith(
      expect.stringContaining('agent was archived before reaching activity state idle'),
    )
  })

  it('returns an archived agent when archived was requested', async () => {
    const command = cli(() => Response.json(agentPayload('archived')))
    await command.run('--activity-state', 'idle', '--activity-state', 'archived', '--json')
    expect(console.log).toHaveBeenCalledWith(printed('archived'))
    expect(console.error).not.toHaveBeenCalled()
  })

  it('fails when the timeout elapses before the activity matches', async () => {
    const command = cli(() => Response.json(agentPayload('running')))
    vi.useFakeTimers()
    const run = command.run('--timeout', '1')
    await vi.advanceTimersByTimeAsync(1_000)
    await run
    expect(process.exitCode).toBe(1)
    expect(console.error).toHaveBeenCalledWith(
      expect.stringContaining('agent did not reach activity state idle within 1s'),
    )
  })

  it('aborts an in-flight request one poll interval after the timeout', async () => {
    const command = cli(
      (request) =>
        new Promise<Response>((_, reject) => {
          request.signal.addEventListener(
            'abort',
            () => {
              reject(new Error('aborted'))
            },
            { once: true },
          )
        }),
    )
    vi.useFakeTimers()
    const run = command.run('--timeout', '1')
    await vi.advanceTimersByTimeAsync(2_999)
    expect(command.requests[0]?.signal.aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    await run
    expect(command.requests[0]?.signal.aborted).toBe(true)
    expect(process.exitCode).toBe(1)
    expect(console.error).toHaveBeenCalledWith(
      expect.stringContaining('agent did not reach activity state idle within 1s'),
    )
  })

  it('counts a match from the last poll that lands just after the timeout', async () => {
    let release: (() => void) | undefined
    const command = cli((request) => {
      if (command.requests.length === 1) return Response.json(agentPayload('running'))
      expect(request.signal.aborted).toBe(false)
      return new Promise<Response>((resolve) => {
        release = () => {
          resolve(Response.json(agentPayload('idle')))
        }
      })
    })
    vi.useFakeTimers()
    const run = command.run('--timeout', '1', '--json')
    await vi.advanceTimersByTimeAsync(1_000)
    expect(command.requests).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1_500)
    release?.()
    await run
    expect(command.requests[1]?.signal.aborted).toBe(false)
    expect(console.log).toHaveBeenCalledWith(printed('idle'))
    expect(console.error).not.toHaveBeenCalled()
  })

  it('does not succeed on a response that lands after the grace window before its timer fires', async () => {
    let release: (() => void) | undefined
    const command = cli(
      () =>
        new Promise<Response>((resolve) => {
          release = () => {
            resolve(Response.json(agentPayload('idle')))
          }
        }),
    )
    vi.useFakeTimers()
    const run = command.run('--timeout', '1', '--json')
    await vi.advanceTimersByTimeAsync(0)
    vi.setSystemTime(Date.now() + 3_000)
    release?.()
    await run
    expect(console.log).not.toHaveBeenCalled()
    expect(process.exitCode).toBe(1)
    expect(console.error).toHaveBeenCalledWith(
      expect.stringContaining('agent did not reach activity state idle within 1s'),
    )
  })

  it('does not succeed on a matching response that arrives after the grace window', async () => {
    let release: (() => void) | undefined
    const command = cli(
      () =>
        new Promise<Response>((resolve) => {
          release = () => {
            resolve(Response.json(agentPayload('idle')))
          }
        }),
    )
    vi.useFakeTimers()
    const run = command.run('--timeout', '1', '--json')
    await vi.advanceTimersByTimeAsync(3_000)
    await run
    release?.()
    expect(command.requests[0]?.signal.aborted).toBe(true)
    expect(process.exitCode).toBe(1)
    expect(console.log).not.toHaveBeenCalled()
    expect(console.error).toHaveBeenCalledWith(
      expect.stringContaining('agent did not reach activity state idle within 1s'),
    )
  })
})
