import { ApiError, bearerToken, createOmnaraClient, sdk } from '@omnara/sdk'
import YAML from 'yaml'

import type { AgentProfileSource } from './bootstrap.js'
import {
  agentProfileSourceFromConfig,
  createDaemonToken,
  ensureAgentProfile,
  ensureMachine,
  ensureMachineGrant,
  ensureModelAccess,
  ensureOrg,
  ensureProject,
  launchAgent,
  listResumableAgents,
  loadAgentProfileSource,
  waitForMachineConnection,
} from './bootstrap.js'
import { runChat } from './chat.js'
import type { DaemonIdentity } from './daemon.js'
import { loadDaemonState, saveDaemonState, startDaemon, stopDaemon, writeDaemonConfig } from './daemon.js'
import type { CliEnv } from './env.js'
import { loadEnv } from './env.js'
import { activeStep, complete, interruptForError, progress } from './progress.js'
import { pickOne } from './select.js'
import { applyStoredOverrides, loadCliState, recordAgent, updateCliState } from './state.js'

let lastRequest: string | undefined

async function bootstrapDaemonIdentity(env: CliEnv, daemonToken: string): Promise<DaemonIdentity | undefined> {
  const daemonClient = createOmnaraClient({ baseUrl: env.apiUrl, auth: bearerToken(daemonToken) })
  try {
    const { data } = await sdk.bootstrapDaemon({ client: daemonClient })
    return { installationId: data.installation_id, machineId: data.machine_id }
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) return undefined
    throw error
  }
}

async function main(): Promise<void> {
  const mode = process.argv[2] === 'resume' ? 'resume' : 'start'
  if (process.argv[2] != null && mode !== 'resume') {
    throw new Error(`unknown argument ${JSON.stringify(process.argv[2])}; usage: pnpm run start [resume]`)
  }
  const env = loadEnv()
  const client = createOmnaraClient({ baseUrl: env.apiUrl, auth: bearerToken(env.apiKey) })
  client.interceptors.request.use((request) => {
    lastRequest = `${request.method} ${request.url}`
    return request
  })
  console.error(`api: ${env.apiUrl}`)
  const cliState = loadCliState(env)

  const orgId = await ensureOrg(client, env, cliState.orgId)
  updateCliState(env, { orgId })
  const projectId = await ensureProject(client, orgId, env.projectName)
  const machine = await ensureMachine(client, orgId)
  await ensureMachineGrant(client, orgId, projectId, machine.id)

  let daemon: ReturnType<typeof startDaemon> | undefined
  if (machine.connection_state === 'online') {
    complete('daemon', 'already attached and online')
  } else {
    let state = loadDaemonState(env)
    if (state == null || state.orgId !== orgId || state.machineId !== machine.id) {
      const daemonToken = await createDaemonToken(client, orgId, machine.id)
      state = { apiUrl: env.apiUrl, orgId, machineId: machine.id, daemonToken }
      saveDaemonState(env, state)
    }
    progress('daemon', 'Validating daemon token...')
    let identity = await bootstrapDaemonIdentity(env, state.daemonToken)
    if (identity == null) {
      const daemonToken = await createDaemonToken(client, orgId, machine.id)
      state = { apiUrl: env.apiUrl, orgId, machineId: machine.id, daemonToken }
      saveDaemonState(env, state)
      progress('daemon', 'Validating daemon token...')
      identity = await bootstrapDaemonIdentity(env, state.daemonToken)
      if (identity == null) throw new Error('freshly created daemon token was rejected by the API')
    }
    writeDaemonConfig(env, state.daemonToken, identity)
    progress('daemon', 'Starting omnarad...')
    daemon = startDaemon(env, state.daemonToken)
    progress('daemon', 'Waiting for the machine to come online...')
    if (await waitForMachineConnection(client, orgId, machine.id, 60_000)) {
      complete('daemon', 'online')
    } else {
      complete('daemon', 'did not come online within 60s; continuing anyway')
    }
  }
  process.on('exit', () => {
    if (daemon != null) stopDaemon(daemon)
  })

  let agentId: string
  let chatProfile: AgentProfileSource
  if (mode === 'resume') {
    const agents = await listResumableAgents(client, orgId, projectId)
    if (agents.length === 0) {
      throw new Error('no active agents to resume; run "pnpm run start" to launch one')
    }
    const known = new Map((loadCliState(env).agents ?? []).map((agent) => [agent.id, agent]))
    const index = await pickOne(
      'Resume which agent?',
      agents.map((agent) => ({
        label: agent.name.trim() !== '' ? agent.name : agent.id,
        hint: `${known.get(agent.id)?.profileName ?? 'unknown profile'}, created ${agent.created_at}`,
      })),
    )
    const agent = agents[index]!
    complete('agent', `${agent.name.trim() !== '' ? agent.name : 'agent'} (${agent.id}, resumed)`)
    agentId = agent.id
    if (agent.current_config_id == null) {
      throw new Error(`agent ${agent.id} has no config to resume with`)
    }
    chatProfile = await agentProfileSourceFromConfig(client, orgId, projectId, agent.current_config_id)
  } else {
    const cwd = process.cwd()
    const { profile: profileSource } = await loadAgentProfileSource(env.profilePath, machine.display_name, cwd)
    applyStoredOverrides(profileSource, loadCliState(env))
    await ensureModelAccess(client, orgId, projectId, profileSource)
    const profileName = profileSource.name?.trim() || 'cli-agent'
    const { profile } = await ensureAgentProfile(client, orgId, projectId, profileName, YAML.stringify(profileSource))
    const launch = await launchAgent(client, orgId, projectId, profile)
    agentId = launch.agent.id
    chatProfile = profileSource
    recordAgent(env, {
      id: agentId,
      profileName,
      createdAt: launch.agent.created_at,
      cwd,
    })
    console.error(`cwd: ${cwd}`)
  }
  console.error('')
  console.error('type a prompt; use @file.txt or @[path with spaces] to attach files,')
  console.error('$[skill-name] to ask the agent to use a skill, and /quit to exit')
  console.error('config commands: /model <model-slug>, /effort <level>, /permission [tool] <ask|allow>,')
  console.error('                 /display <simple|default|full>')
  console.error('')

  await runChat({ client, env, orgId, projectId, agentId, profile: chatProfile })
  if (daemon != null) stopDaemon(daemon)
}

main().catch((error: unknown) => {
  interruptForError()
  const step = activeStep()
  if (step != null) console.error(`error: failed while setting up ${JSON.stringify(step)}`)
  if (error instanceof ApiError) {
    console.error(`error: ${lastRequest ?? 'API request'} failed with HTTP ${error.status}`)
    console.error(`  code:    ${error.code ?? 'none'}`)
    console.error(`  message: ${error.message}`)
    if (error.status === 404 && error.code == null) {
      console.error('  hint:    a 404 without an API error code usually means OMNARA_API_URL does not point')
      console.error('           at the Omnara API (use the API origin, e.g. http://localhost:8080)')
    }
    if (error.status === 401) {
      console.error('  hint:    check that OMNARA_API_KEY is a valid, unrevoked personal access token or org API key')
    }
    if (error.status === 403) {
      console.error("  hint:    the token's user lacks permission for this operation; org-level resources")
      console.error('           (machines, daemon tokens, model providers) need an org admin role')
    }
  } else if (error instanceof TypeError && lastRequest != null && error.message === 'fetch failed') {
    console.error(`error: could not reach the API (${lastRequest}): ${(error.cause as Error | undefined)?.message ?? error.message}`)
  } else {
    console.error(`error: ${error instanceof Error ? error.message : String(error)}`)
  }
  process.exit(1)
})
