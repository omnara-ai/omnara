import { readFile } from 'node:fs/promises'
import os from 'node:os'

import type {
  Agent,
  AgentConfig,
  AgentProfile,
  LaunchAgentResponse,
  MachinePool,
  OmnaraClient,
  ProjectMachinePoolGrant,
} from '@omnara/sdk'
import { sdk } from '@omnara/sdk'
import YAML from 'yaml'

import type { CliEnv } from './env.js'
import { complete, progress } from './progress.js'
import { pickOne } from './select.js'

export interface AgentProfileSource {
  name?: string
  model?: {
    name?: string
    reasoning?: { effort: string }
    [key: string]: unknown
  }
  machine_sources?: unknown[]
  tools?: Record<string, AgentProfileToolSource>
  [key: string]: unknown
}

export interface AgentProfileToolSource {
  type?: string
  permission?: { mode: string; parameters?: Record<string, unknown> }
  [key: string]: unknown
}

export async function ensureOrg(client: OmnaraClient, env: CliEnv): Promise<string> {
  progress('org', 'Listing...')
  const { data } = await sdk.getCurrentUser({ client })
  const orgs = data.orgs
  if (env.orgId != null) {
    const match = orgs.find((org) => org.id === env.orgId)
    if (match == null) {
      throw new Error(`OMNARA_ORG_ID ${env.orgId} is not one of this user's organizations`)
    }
    complete('org', `${match.name} (${match.id})`)
    return match.id
  }
  if (orgs.length === 1) {
    const org = orgs[0]!
    complete('org', `${org.name} (${org.id})`)
    return org.id
  }
  if (orgs.length === 0) {
    throw new Error('this token has no organizations; create one in the Omnara app first')
  }
  complete('org', `${orgs.length} available`)
  const index = await pickOne(
    'Select an organization:',
    orgs.map((org) => ({ label: org.name, hint: `${org.id}, ${org.role}` })),
  )
  const org = orgs[index]!
  complete('org', `${org.name} (${org.id})`)
  return org.id
}

export async function ensureProject(client: OmnaraClient, orgId: string, name: string): Promise<string> {
  progress('project', 'Looking up...')
  let cursor: string | undefined
  do {
    const { data } = await sdk.listVisibleProjects({ client, path: { orgID: orgId }, query: { cursor } })
    const match = data.data.find((project) => project.name === name)
    if (match != null) {
      complete('project', `${match.name} (${match.id})`)
      return match.id
    }
    cursor = data.next_cursor ?? undefined
  } while (cursor != null)
  progress('project', 'Creating...')
  const { data } = await sdk.createProject({
    client,
    path: { orgID: orgId },
    headers: { 'Idempotency-Key': `cli-agent-project-${name}` },
    body: { name },
  })
  complete('project', `${data.name} (${data.id})`)
  return data.id
}

export interface EnsuredMachine {
  id: string
  display_name: string
  connection_state: 'online' | 'asleep' | 'offline'
}

export type MachineTarget =
  | { kind: 'pool'; pool: MachinePool }
  | { kind: 'local'; machine: EnsuredMachine }
  | { kind: 'remote'; machine: EnsuredMachine }

export async function selectMachineTarget(
  client: OmnaraClient,
  orgId: string,
  preselected?: 'pool' | 'local',
): Promise<MachineTarget> {
  const choice =
    preselected === 'pool'
      ? 0
      : preselected === 'local'
        ? 1
        : await pickOne('Where should the agent run commands?', [
            { label: 'Pool machine', hint: 'launch machines from an org machine pool' },
            { label: 'Local machine', hint: 'register this host and run omnarad for the session' },
            { label: 'Machine daemon', hint: 'connect to an existing machine running its own daemon' },
          ])
  switch (choice) {
    case 0:
      return { kind: 'pool', pool: await pickMachinePool(client, orgId) }
    case 1:
      return { kind: 'local', machine: await ensureMachine(client, orgId) }
    default:
      return { kind: 'remote', machine: await pickDaemonMachine(client, orgId) }
  }
}

async function pickMachinePool(client: OmnaraClient, orgId: string): Promise<MachinePool> {
  progress('machine pool', 'Listing...')
  const pools: MachinePool[] = []
  let cursor: string | undefined
  do {
    const { data } = await sdk.listMachinePools({ client, path: { orgID: orgId }, query: { cursor } })
    pools.push(...data.data)
    cursor = data.next_cursor ?? undefined
  } while (cursor != null)
  if (pools.length === 0) {
    throw new Error('no machine pools in this org; create one first or pick another machine option')
  }
  if (pools.length === 1) {
    const pool = pools[0]!
    complete('machine pool', `${pool.name} (${pool.id})`)
    return pool
  }
  complete('machine pool', `${pools.length} available`)
  const index = await pickOne(
    'Select a machine pool:',
    pools.map((pool) => ({ label: pool.name, hint: `${pool.provider}, ${pool.id}` })),
  )
  const pool = pools[index]!
  complete('machine pool', `${pool.name} (${pool.id})`)
  return pool
}

async function pickDaemonMachine(client: OmnaraClient, orgId: string): Promise<EnsuredMachine> {
  progress('machine', 'Listing...')
  const machines: EnsuredMachine[] = []
  let cursor: string | undefined
  do {
    const { data } = await sdk.listVisibleMachines({
      client,
      path: { orgID: orgId },
      query: { source_kind: 'byo', cursor },
    })
    machines.push(...data.data.filter((machine) => machine.deleted_at == null))
    cursor = data.next_cursor ?? undefined
  } while (cursor != null)
  if (machines.length === 0) {
    throw new Error('no machines registered in this org; start a machine daemon first or pick the local machine option')
  }
  if (machines.length === 1) {
    const machine = machines[0]!
    complete('machine', `${machine.display_name} (${machine.id}, ${machine.connection_state})`)
    return machine
  }
  complete('machine', `${machines.length} available`)
  const index = await pickOne(
    'Select a machine:',
    machines.map((machine) => ({ label: machine.display_name, hint: `${machine.id}, ${machine.connection_state}` })),
  )
  const machine = machines[index]!
  complete('machine', `${machine.display_name} (${machine.id}, ${machine.connection_state})`)
  return machine
}

async function ensureMachine(client: OmnaraClient, orgId: string): Promise<EnsuredMachine> {
  const displayName = os.hostname()
  progress('machine', 'Looking up...')
  const { data } = await sdk.listVisibleMachines({
    client,
    path: { orgID: orgId },
    query: { name: displayName.replaceAll('*', '\\*').replaceAll('?', '\\?'), source_kind: 'byo' },
  })
  const match = data.data.find((machine) => machine.display_name === displayName && machine.deleted_at == null)
  if (match != null) {
    complete('machine', `${match.display_name} (${match.id}, ${match.connection_state})`)
    return match
  }
  progress('machine', 'Registering...')
  const { data: machine } = await sdk.createMachine({
    client,
    path: { orgID: orgId },
    headers: { 'Idempotency-Key': `cli-agent-machine-${displayName}` },
    body: {
      display_name: displayName,
      description: 'Machine registered by the cli-agent example',
      cwd: os.homedir(),
    },
  })
  complete('machine', `${machine.display_name} (${machine.id}, ${machine.connection_state})`)
  return machine
}

export async function createDaemonToken(client: OmnaraClient, orgId: string, machineId: string): Promise<string> {
  progress('daemon token', 'Creating...')
  const { data } = await sdk.createByoMachineDaemonToken({
    client,
    path: { orgID: orgId, machineID: machineId },
    body: { name: 'cli-agent' },
  })
  complete('daemon token', `${data.token_record.name} (${data.token_record.id})`)
  return data.token
}

export async function ensureMachineGrant(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  machineId: string,
): Promise<void> {
  progress('machine grant', 'Checking...')
  let cursor: string | undefined
  do {
    const { data } = await sdk.listProjectMachineGrants({
      client,
      path: { orgID: orgId, projectID: projectId },
      query: { cursor },
    })
    const match = data.data.find((item) => item.grant.machine_id === machineId)
    if (match != null) {
      complete('machine grant', match.grant.id)
      return
    }
    cursor = data.next_cursor ?? undefined
  } while (cursor != null)
  progress('machine grant', 'Creating...')
  const { data } = await sdk.createProjectMachineGrant({
    client,
    path: { orgID: orgId, projectID: projectId },
    headers: { 'Idempotency-Key': `cli-agent-machine-grant-${machineId}` },
    body: { machine_id: machineId },
  })
  complete('machine grant', data.grant.id)
}

export async function ensureMachinePoolGrant(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  poolId: string,
  repoCredSecretId?: string,
): Promise<void> {
  progress('pool grant', 'Checking...')
  let existing: ProjectMachinePoolGrant | undefined
  let cursor: string | undefined
  do {
    const { data } = await sdk.listProjectMachinePoolGrants({
      client,
      path: { orgID: orgId, projectID: projectId },
      query: { cursor },
    })
    existing = data.data.find((item) => item.grant.machine_pool_id === poolId)?.grant
    if (existing != null) break
    cursor = data.next_cursor ?? undefined
  } while (cursor != null)
  if (existing == null) {
    progress('pool grant', 'Creating...')
    const { data } = await sdk.createProjectMachinePoolGrant({
      client,
      path: { orgID: orgId, projectID: projectId },
      headers: { 'Idempotency-Key': `cli-agent-pool-grant-${poolId}` },
      body: {
        machine_pool_id: poolId,
        ...(repoCredSecretId != null ? { default_machine_secret_env_overlay: { REPO_CRED: repoCredSecretId } } : {}),
      },
    })
    complete('pool grant', data.id)
    return
  }
  const overlay = { ...existing.default_machine_secret_env_overlay }
  const changed = repoCredSecretId != null ? overlay.REPO_CRED !== repoCredSecretId : 'REPO_CRED' in overlay
  if (!changed) {
    complete('pool grant', existing.id)
    return
  }
  if (repoCredSecretId != null) overlay.REPO_CRED = repoCredSecretId
  else delete overlay.REPO_CRED
  progress('pool grant', 'Updating...')
  await sdk.updateProjectMachinePoolGrant({
    client,
    path: { orgID: orgId, projectID: projectId, poolGrantID: existing.id },
    body: { default_machine_secret_env_overlay: overlay },
  })
  complete('pool grant', `${existing.id} (repo credential ${repoCredSecretId != null ? 'set' : 'removed'})`)
}

export async function ensureModelGrants(client: OmnaraClient, orgId: string, projectId: string): Promise<void> {
  progress('model grants', 'Listing models...')
  const { data: configs } = await sdk.listModelProviderConfigs({ client, path: { orgID: orgId } })
  const modelIds: string[] = []
  for (const config of configs.data) {
    let cursor: string | undefined
    do {
      const { data } = await sdk.listConfiguredModels({
        client,
        path: { orgID: orgId, modelProviderConfigID: config.id },
        query: { cursor },
      })
      for (const model of data.data) modelIds.push(model.id)
      cursor = data.next_cursor ?? undefined
    } while (cursor != null)
  }
  if (modelIds.length === 0) {
    throw new Error('no configured models in this org; create a model provider config and models first')
  }
  progress('model grants', 'Checking...')
  const granted = new Set<string>()
  let cursor: string | undefined
  do {
    const { data } = await sdk.listProjectModelGrants({
      client,
      path: { orgID: orgId, projectID: projectId },
      query: { cursor },
    })
    for (const item of data.data) granted.add(item.grant.configured_model_id)
    cursor = data.next_cursor ?? undefined
  } while (cursor != null)
  const missing = modelIds.filter((id) => !granted.has(id))
  if (missing.length > 0) progress('model grants', 'Creating...')
  for (const id of missing) {
    await sdk.createProjectModelGrant({
      client,
      path: { orgID: orgId, projectID: projectId },
      headers: { 'Idempotency-Key': `cli-agent-model-grant-${projectId}-${id}` },
      body: { configured_model_id: id },
    })
  }
  complete('model grants', `${modelIds.length} models (${missing.length} new)`)
}

export interface RepoSetup {
  uri: string
  credSecretId?: string
}

const repoCloneDir = '/workspace/repo'
const repoCredSecretName = 'cli-agent-repo-cred'

export async function ensureRepoCredSecret(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  value: string,
): Promise<string> {
  progress('repo secret', 'Checking...')
  const { data } = await sdk.listSecrets({
    client,
    path: { orgID: orgId },
    query: { owner_kind: 'project', owner_project_id: projectId, name: repoCredSecretName },
  })
  const match = data.data.find((secret) => secret.name === repoCredSecretName)
  if (match != null) {
    await sdk.createSecretVersion({
      client,
      path: { orgID: orgId, secretID: match.id },
      body: { material: { kind: 'generic', value } },
    })
    complete('repo secret', `${match.name} (${match.id}, updated)`)
    return match.id
  }
  progress('repo secret', 'Creating...')
  const { data: secret } = await sdk.createSecret({
    client,
    path: { orgID: orgId },
    headers: { 'Idempotency-Key': `cli-agent-repo-cred-${projectId}` },
    body: {
      owner: { kind: 'project', project_id: projectId },
      name: repoCredSecretName,
      material: { kind: 'generic', value },
    },
  })
  complete('repo secret', `${secret.name} (${secret.id})`)
  return secret.id
}

function cloneStartupScript(repo: RepoSetup): string {
  let cloneUrl = `'${repo.uri.replaceAll("'", String.raw`'\''`)}'`
  if (repo.credSecretId != null && repo.uri.startsWith('https://')) {
    cloneUrl = `"https://x-access-token:\${REPO_CRED}@${repo.uri.slice('https://'.length)}"`
  }
  return [
    '#!/bin/sh',
    'set -eu',
    `if [ ! -d ${repoCloneDir}/.git ]; then`,
    `  git clone --depth 1 ${cloneUrl} ${repoCloneDir}`,
    'fi',
    '',
  ].join('\n')
}

export async function loadAgentProfileSource(
  profilePath: string,
  target: MachineTarget,
  cwd: string,
  repo?: RepoSetup,
): Promise<AgentProfileSource> {
  const raw = await readFile(profilePath, 'utf8')
  const profile = YAML.parse(raw) as AgentProfileSource | null
  if (profile == null || typeof profile !== 'object') {
    throw new Error(`${profilePath} must contain an agent config object`)
  }
  if (profile.machine_sources == null) {
    switch (target.kind) {
      case 'pool':
        profile.machine_sources = [
          {
            machine_pool_name: target.pool.name,
            ...(repo != null
              ? {
                  cwd: repoCloneDir,
                  machine_provider_options_overlay: { startup_script: cloneStartupScript(repo) },
                }
              : {}),
          },
        ]
        break
      case 'local':
        profile.machine_sources = [{ machine_name: target.machine.display_name, cwd }]
        break
      case 'remote':
        profile.machine_sources = [{ machine_name: target.machine.display_name }]
        break
    }
  }
  const note =
    target.kind === 'pool'
      ? repo != null
        ? `The agent's commands run on machines from the ${target.pool.name} machine pool, ` +
          `inside a clone of ${repo.uri} at ${repoCloneDir}.`
        : `The agent's commands run on machines from the ${target.pool.name} machine pool.`
      : target.kind === 'local'
        ? `The user's working directory on ${target.machine.display_name} is ${cwd}.`
        : `The agent's commands run on the machine ${target.machine.display_name}.`
  const instruction = typeof profile.instruction === 'string' ? profile.instruction.trimEnd() : ''
  profile.instruction = `${instruction}\n${note}\n`
  return profile
}

export async function listResumableAgents(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
): Promise<Agent[]> {
  progress('agents', 'Listing...')
  const agents: Agent[] = []
  let cursor: string | undefined
  do {
    const { data } = await sdk.listAgents({
      client,
      path: { orgID: orgId, projectID: projectId },
      query: { cursor },
    })
    agents.push(...data.data.filter((agent) => agent.state === 'active'))
    cursor = data.next_cursor ?? undefined
  } while (cursor != null && agents.length < 50)
  complete('agents', `${agents.length} active`)
  return agents
}

export async function agentProfileSourceFromConfig(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  agentConfigId: string,
): Promise<AgentProfileSource> {
  const { data: config } = await sdk.getAgentConfig({
    client,
    path: { orgID: orgId, projectID: projectId, agentConfigID: agentConfigId },
  })
  if (config.source == null) {
    throw new Error(`agent config ${agentConfigId} has no source to resume from`)
  }
  const profile = YAML.parse(config.source) as AgentProfileSource | null
  if (profile == null || typeof profile !== 'object') {
    throw new Error(`agent config ${agentConfigId} source is not an agent config object`)
  }
  return profile
}

export async function ensureAgentProfile(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  profileName: string,
  profileSource: AgentProfileSource,
): Promise<{ profile: AgentProfile; config: AgentConfig }> {
  progress('agent config', 'Compiling...')
  const { data: config } = await sdk.createAgentConfig({
    client,
    path: { orgID: orgId, projectID: projectId },
    body: { source: JSON.stringify(profileSource), source_format: 'json' },
  })
  complete('agent config', config.id)

  progress('agent profile', 'Looking up...')
  const { data: profiles } = await sdk.listAgentProfiles({
    client,
    path: { orgID: orgId, projectID: projectId },
    query: { name: profileName.replaceAll('*', '\\*').replaceAll('?', '\\?') },
  })
  const existing = profiles.data.find((profile) => profile.name === profileName)
  if (existing == null) {
    progress('agent profile', 'Creating...')
    const { data: profile } = await sdk.createAgentProfile({
      client,
      path: { orgID: orgId, projectID: projectId },
      headers: { 'Idempotency-Key': `cli-agent-profile-${profileName}` },
      body: { name: profileName, config: config.id },
    })
    complete('agent profile', `${profile.name} (${profile.id})`)
    return { profile, config }
  }
  if (existing.current_config_id === config.id) {
    complete('agent profile', `${existing.name} (${existing.id}, unchanged)`)
    return { profile: existing, config }
  }
  progress('agent profile', 'Updating...')
  const { data: profile } = await sdk.updateAgentProfile({
    client,
    path: { orgID: orgId, projectID: projectId, agentProfileID: existing.id },
    body: { config: config.id, expected_current_config_id: existing.current_config_id },
  })
  complete('agent profile', `${profile.name} (${profile.id}, updated)`)
  return { profile, config }
}

export async function launchAgent(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  profile: AgentProfile,
): Promise<LaunchAgentResponse> {
  progress('agent', 'Launching...')
  const { data } = await sdk.createAgent({
    client,
    path: { orgID: orgId, projectID: projectId },
    body: { profile: profile.id, config: profile.current_config_id },
  })
  complete('agent', `${data.agent.name || 'agent'} (${data.agent.id})`)
  return data
}

export async function waitForMachineConnection(
  client: OmnaraClient,
  orgId: string,
  machineId: string,
  timeoutMs: number,
): Promise<boolean> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const { data: machine } = await sdk.getMachine({ client, path: { orgID: orgId, machineID: machineId } })
    if (machine.connection_state === 'online') return true
    await new Promise((resolve) => setTimeout(resolve, 1000))
  }
  return false
}
