import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

import {
  type AgentEvent,
  bearerToken,
  createOmnaraClient,
  type ModelUsage,
  type OmnaraClient,
  sdk,
} from '@omnara/sdk'

export interface Omnara {
  client: OmnaraClient
  orgID: string
  projectID: string
}

interface CliConfigFile {
  api_url?: string
  token?: string
  org_id?: string
  project_id?: string
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

const RETRYABLE_STATUS = new Set([429, 502, 503, 504])

/**
 * Retries gateway errors and dropped connections with backoff. Reads and
 * file uploads (PUT) are safe to repeat; other writes are repeated only when they carry an
 * Idempotency-Key, so a retry can't launch a second agent.
 */
const retryingFetch: typeof fetch = async (input, init) => {
  const request = new Request(input, init)
  const safe = ['GET', 'PUT'].includes(request.method) || request.headers.has('Idempotency-Key')
  const body = request.body === null ? undefined : await request.arrayBuffer()
  for (let attempt = 0; ; attempt++) {
    try {
      const response = await fetch(new Request(request, { body }))
      if (!safe || attempt >= 5 || !RETRYABLE_STATUS.has(response.status)) return response
    } catch (error) {
      if (!safe || attempt >= 5) throw error
    }
    await sleep(Math.min(30_000, 1000 * 2 ** attempt) * (0.5 + Math.random()))
  }
}

/**
 * Uses OMNARA_API_KEY / OMNARA_API_URL / OMNARA_ORG_ID / OMNARA_PROJECT_ID
 * when set, and otherwise the login saved by `omnara login`.
 */
export function connect(overrides: { orgID?: string; projectID?: string }): Omnara {
  let file: CliConfigFile = {}
  try {
    file = JSON.parse(readFileSync(join(homedir(), '.config', 'omnara', 'config.json'), 'utf8'))
  } catch {
    // No saved CLI login; environment variables must cover everything.
  }
  const token = process.env.OMNARA_API_KEY ?? file.token
  const baseUrl = process.env.OMNARA_API_URL ?? file.api_url ?? 'https://api.omnara.com/v1'
  const orgID = overrides.orgID ?? process.env.OMNARA_ORG_ID ?? file.org_id
  const projectID = overrides.projectID ?? process.env.OMNARA_PROJECT_ID ?? file.project_id
  if (token === undefined) throw new Error("not logged in: run 'omnara login' or set OMNARA_API_KEY")
  if (orgID === undefined || projectID === undefined) {
    throw new Error('pass --org and --project, or set OMNARA_ORG_ID and OMNARA_PROJECT_ID')
  }
  const client = createOmnaraClient({
    baseUrl,
    auth: bearerToken(token),
    headers: { 'User-Agent': 'omnara-terminal-bench' },
    fetch: retryingFetch,
  })
  return { client, orgID, projectID }
}


export async function createRunProfile(omnara: Omnara, name: string, configSource: string): Promise<string> {
  const { client, orgID, projectID } = omnara
  const path = { orgID, projectID }
  const config = await sdk.createAgentConfig({
    client,
    path,
    body: { source: configSource, source_format: 'yaml' },
  })
  const profile = await sdk.createAgentProfile({
    client,
    path,
    body: { name, config: config.data.id },
  })
  return profile.data.id
}

export interface LaunchedTask {
  agentID: string
  machineID: string
}

/**
 * Launches the task's agent under the run's profile with no input yet. The
 * launch provisions the task machine from the pool; the instruction is sent
 * only once that machine is online, so the agent never starts without it.
 */
export async function launchTaskAgent(
  omnara: Omnara,
  options: { profileID: string; name: string; configSource: string },
): Promise<LaunchedTask> {
  const { client, orgID, projectID } = omnara
  const path = { orgID, projectID }
  // Compile first: launching with a profile and an inline config_source is
  // rejected ("profile-attributed derived launch requires a base config").
  const config = await sdk.createAgentConfig({
    client,
    path,
    body: { source: options.configSource, source_format: 'yaml' },
  })
  // A full pool rejects the launch; wait for running tasks to free machines.
  const deadline = Date.now() + 3 * 60 * 60 * 1000
  let launched
  for (;;) {
    const attempt = await sdk.createAgent({
      client,
      path,
      headers: { 'Idempotency-Key': randomUUID() },
      body: { profile: options.profileID, config: config.data.id, name: options.name },
      throwOnError: false,
    })
    if (attempt.data !== undefined) {
      launched = attempt
      break
    }
    const reason = attempt.error instanceof Error ? attempt.error.message : JSON.stringify(attempt.error)
    if (!reason.includes('capacity exceeded') || Date.now() > deadline) {
      throw attempt.error
    }
    await sleep(20_000 + Math.random() * 20_000)
  }
  let machineID: string | undefined
  if ('machine_bindings' in launched.data) {
    machineID = launched.data.machine_bindings.find((binding) => binding.machine_id !== undefined)?.machine_id
  } else {
    // A retried launch replays the first one's agent without its bindings.
    const current = await sdk.getAgent({ client, path: { ...path, agentID: launched.data.agent.id } })
    machineID = current.data.machine_ids[0]
  }
  if (machineID === undefined) throw new Error('launch did not provision a machine')
  return { agentID: launched.data.agent.id, machineID }
}

export async function waitForMachineOnline(omnara: Omnara, machineID: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const result = await sdk.getMachine({
      client: omnara.client,
      path: { orgID: omnara.orgID, machineID },
      throwOnError: false,
    })
    if (result.error !== undefined) {
      // A pool machine reads as 404 until the provider has created it, which
      // can take minutes when many launch at once.
      if (result.response?.status === 404 && Date.now() < deadline) {
        await sleep(5000)
        continue
      }
      throw result.error
    }
    const machine = result.data
    if (machine.connection_state === 'online') return
    if (machine.lifecycle_state === 'provision_failed' || machine.lifecycle_state.startsWith('delet')) {
      const reason = machine.failure_report ?? machine.lifecycle_reason_message ?? machine.lifecycle_reason_code
      throw new Error(`machine ${machineID} is ${machine.lifecycle_state}: ${JSON.stringify(reason)}`)
    }
    if (Date.now() > deadline) {
      throw new Error(`machine ${machineID} did not come online within ${timeoutMs / 1000}s (${machine.lifecycle_state}, ${machine.connection_state})`)
    }
    await sleep(3000)
  }
}

export async function sendInstruction(omnara: Omnara, agentID: string, text: string): Promise<void> {
  await sdk.createAgentInput({
    client: omnara.client,
    path: { orgID: omnara.orgID, projectID: omnara.projectID, agentID },
    headers: { 'Idempotency-Key': randomUUID() },
    body: { content_blocks: [{ type: 'text', text }] },
  })
}

export async function listAllEvents(omnara: Omnara, agentID: string): Promise<AgentEvent[]> {
  const events: AgentEvent[] = []
  let after: number | undefined
  for (;;) {
    const page = await sdk.listEvents({
      client: omnara.client,
      path: { orgID: omnara.orgID, projectID: omnara.projectID, agentID },
      query: { after_sequence: after, limit: 500 },
    })
    events.push(...page.data.data)
    if (page.data.data.length === 0 || page.data.next_after_sequence === after) return events
    after = page.data.next_after_sequence
  }
}

export type AgentOutcome = 'completed' | 'timed_out' | 'blocked' | 'failed'

/**
 * Waits until the agent has answered the instruction: an idle agent whose last
 * model output ended its turn (any stop reason other than tool_use). Polls the
 * cheap agent read and only pages events once the agent looks idle. Cancels
 * once `deadline` (epoch ms) passes.
 */
export async function waitForAgent(omnara: Omnara, agentID: string, deadline: number): Promise<AgentOutcome> {
  const path = { orgID: omnara.orgID, projectID: omnara.projectID, agentID }
  let after: number | undefined
  let finished = false
  for (;;) {
    const { data } = await sdk.getAgent({ client: omnara.client, path })
    const state = data.agent.activity?.state
    if (state === 'waiting_on_interaction') return 'blocked'
    if (state === 'archived') return 'failed'
    if (state === 'idle') {
      for (;;) {
        const page = await sdk.listEvents({ client: omnara.client, path, query: { after_sequence: after, limit: 500 } })
        for (const event of page.data.data) {
          if (event.event_kind === 'model_output') finished = event.stop_reason !== 'tool_use'
          else if (event.event_kind === 'agent_input' || event.event_kind === 'tool_result') finished = false
        }
        if (page.data.data.length === 0 || page.data.next_after_sequence === after) break
        after = page.data.next_after_sequence
      }
      if (finished) return 'completed'
    }
    if (Date.now() > deadline) {
      await sdk.cancelAgent({ client: omnara.client, path, body: {} }).catch(() => undefined)
      return 'timed_out'
    }
    await sleep(10_000)
  }
}

export interface UsageTotals {
  model_calls: number
  input_tokens: number
  cached_input_tokens: number
  output_tokens: number
  reasoning_output_tokens: number
}

export function summarizeUsage(events: AgentEvent[]): UsageTotals {
  const totals: UsageTotals = {
    model_calls: 0,
    input_tokens: 0,
    cached_input_tokens: 0,
    output_tokens: 0,
    reasoning_output_tokens: 0,
  }
  for (const event of events) {
    if (event.event_kind !== 'model_output') continue
    totals.model_calls += 1
    const usage: ModelUsage | undefined = event.usage
    totals.input_tokens += usage?.input_tokens_total ?? 0
    totals.cached_input_tokens += usage?.cache_read_input_tokens ?? 0
    totals.output_tokens += usage?.output_tokens_total ?? 0
    totals.reasoning_output_tokens += usage?.reasoning_output_tokens ?? 0
  }
  return totals
}

export function finalStopReason(events: AgentEvent[]): string | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]
    if (event?.event_kind === 'model_output') return event.stop_reason
  }
  return undefined
}

export function finalMessage(events: AgentEvent[]): string {
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]
    if (event?.event_kind !== 'model_output') continue
    const text = event.content_blocks
      .flatMap((block) => (block.type === 'text' ? [block.text] : []))
      .join('\n')
    if (text.length > 0) return text
  }
  return ''
}

/** Archiving releases the agent's pool machine; the agent stays under its profile. */
export async function archiveAgent(omnara: Omnara, agentID: string): Promise<void> {
  await sdk.archiveAgent({
    client: omnara.client,
    path: { orgID: omnara.orgID, projectID: omnara.projectID, agentID },
  })
}

export interface ExistingAgent {
  agentID: string
  machineID: string | undefined
  /** When the task instruction was sent (epoch ms), if it was. */
  instructedAt: number | undefined
}

/** Active agents under a run profile, keyed by name, for resuming a run. */
export async function listRunAgents(omnara: Omnara, profileID: string): Promise<Map<string, ExistingAgent>> {
  const path = { orgID: omnara.orgID, projectID: omnara.projectID }
  const agents = new Map<string, ExistingAgent>()
  let cursor: string | undefined
  do {
    const page = await sdk.listAgents({
      client: omnara.client,
      path,
      query: { agent_profile_id: profileID, limit: 100, cursor },
    })
    for (const agent of page.data.data) {
      if (agent.state !== 'active') continue
      const [current, events] = await Promise.all([
        sdk.getAgent({ client: omnara.client, path: { ...path, agentID: agent.id } }),
        sdk.listEvents({ client: omnara.client, path: { ...path, agentID: agent.id }, query: { limit: 10 } }),
      ])
      agents.set(agent.name, {
        agentID: agent.id,
        machineID: current.data.machine_ids[0],
        instructedAt: (() => {
          const input = events.data.data.find(
            (event) => event.event_kind === 'agent_input' && event.input_kind === 'content',
          )
          return input === undefined ? undefined : Date.parse(input.created_at)
        })(),
      })
    }
    cursor = page.data.next_cursor ?? undefined
  } while (cursor !== undefined)
  return agents
}
