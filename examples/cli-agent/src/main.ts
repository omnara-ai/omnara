import { ApiError, bearerToken, createOmnaraClient, sdk } from '@omnara/sdk'

import type { AgentProfileSource, RepoSetup } from './bootstrap.js'
import {
  agentProfileSourceFromConfig,
  createDaemonToken,
  ensureAgentProfile,
  ensureMachineGrant,
  ensureMachinePoolGrant,
  ensureModelGrants,
  ensureOrg,
  ensureProject,
  ensureRepoCredSecret,
  launchAgent,
  listResumableAgents,
  loadAgentProfileSource,
  selectMachineTarget,
  waitForMachineConnection,
} from './bootstrap.js'
import { runChat } from './chat.js'
import type { DaemonIdentity } from './daemon.js'
import { loadDaemonState, saveDaemonState, startDaemon, stopDaemon, writeDaemonConfig } from './daemon.js'
import type { CliEnv } from './env.js'
import { loadEnv } from './env.js'
import { activeStep, complete, interruptForError, note, progress } from './progress.js'
import { pickOne } from './select.js'

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
  const usage = 'usage: pnpm run start [resume] [--cloud|--local]'
  let mode: 'start' | 'resume' = 'start'
  let machineChoice: 'pool' | 'local' | undefined
  for (const arg of process.argv.slice(2)) {
    if (arg === 'resume') {
      mode = 'resume'
    } else if (arg === '--cloud' || arg === '--local') {
      const choice = arg === '--cloud' ? 'pool' : 'local'
      if (machineChoice != null && machineChoice !== choice) {
        throw new Error(`--cloud and --local are mutually exclusive; ${usage}`)
      }
      machineChoice = choice
    } else {
      throw new Error(`unknown argument ${JSON.stringify(arg)}; ${usage}`)
    }
  }
  if (mode === 'resume' && machineChoice != null) {
    throw new Error(`--cloud and --local only apply when starting a new agent; ${usage}`)
  }
  const env = loadEnv()
  const client = createOmnaraClient({ baseUrl: env.apiUrl, auth: bearerToken(env.apiKey) })
  client.interceptors.request.use((request) => {
    lastRequest = `${request.method} ${request.url}`
    return request
  })
  note(`api: ${env.apiUrl}`)

  const orgId = await ensureOrg(client, env)
  const projectId = await ensureProject(client, orgId, env.projectName)

  let daemon: ReturnType<typeof startDaemon> | undefined
  let agentId: string
  let chatProfile: AgentProfileSource
  if (mode === 'resume') {
    const agents = await listResumableAgents(client, orgId, projectId)
    if (agents.length === 0) {
      throw new Error('no active agents to resume; run "pnpm run start" to launch one')
    }
    const index = await pickOne(
      'Resume which agent?',
      agents.map((agent) => ({
        label: agent.name.trim() !== '' ? agent.name : agent.id,
        hint: `created ${agent.created_at}`,
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
    const target = await selectMachineTarget(client, orgId, machineChoice)
    let repo: RepoSetup | undefined
    if (target.kind === 'pool' && env.repoUri != null) {
      repo = { uri: env.repoUri }
      if (env.repoCred != null) {
        repo.credSecretId = await ensureRepoCredSecret(client, orgId, projectId, env.repoCred)
      }
    }
    if (target.kind === 'pool') {
      await ensureMachinePoolGrant(client, orgId, projectId, target.pool.id, repo?.credSecretId)
    } else {
      await ensureMachineGrant(client, orgId, projectId, target.machine.id)
    }

    if (target.kind === 'local') {
      const machine = target.machine
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
    }

    const cwd = process.cwd()
    const profileSource = await loadAgentProfileSource(env.profilePath, target, cwd, repo)
    await ensureModelGrants(client, orgId, projectId)
    const profileName = profileSource.name?.trim() || 'cli-agent'
    const { profile } = await ensureAgentProfile(client, orgId, projectId, profileName, profileSource)
    const launch = await launchAgent(client, orgId, projectId, profile)
    agentId = launch.agent.id
    chatProfile = profileSource
    if (target.kind === 'local') note(`cwd: ${cwd}`)
  }
  process.on('exit', () => {
    if (daemon != null) stopDaemon(daemon)
  })
  const modelName = chatProfile.model?.name?.trim() || 'not set'
  const effort = chatProfile.model?.reasoning?.effort
  console.error(`model: ${modelName}${effort != null ? ` (effort ${effort})` : ''}`)
  console.error('')
  console.error('type a prompt; use $[skill-name] to ask the agent to use a skill, and /quit to exit')
  console.error('config commands: /model <model-slug>, /effort <level>, /permission [tool] <ask|allow>,')
  console.error('                 /mode <queue|steer>')
  console.error('')

  await runChat({ client, orgId, projectId, agentId, profile: chatProfile, resume: mode === 'resume' })
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
      console.error('  hint:    check that OMNARA_API_KEY is a valid, unrevoked personal access token')
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
