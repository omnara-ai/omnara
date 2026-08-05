import { readFile } from 'node:fs/promises'
import os from 'node:os'

import type { Agent, AgentConfig, AgentProfile, LaunchAgentResponse, OmnaraClient } from '@omnara/sdk'
import { sdk } from '@omnara/sdk'
import YAML from 'yaml'

import type { CliEnv } from './env.js'
import { complete, progress } from './progress.js'
import { pickOne } from './select.js'

export interface AgentProfileSource {
  name?: string
  model?: {
    provider_config?: string
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

interface ModelProviderPreset {
  preset: 'openai' | 'openrouter' | 'anthropic'
  apiKeyEnv: string
  secretName: string
  contextWindowTokens: number
}

export const reasoningEfforts = ['minimal', 'low', 'medium', 'high']

const modelProviderPresets: Record<string, ModelProviderPreset> = {
  'openai-prod': {
    preset: 'openai',
    apiKeyEnv: 'OPENAI_API_KEY',
    secretName: 'openai-prod-api-key',
    contextWindowTokens: 128000,
  },
  'openrouter-prod': {
    preset: 'openrouter',
    apiKeyEnv: 'OPENROUTER_API_KEY',
    secretName: 'openrouter-prod-api-key',
    contextWindowTokens: 128000,
  },
  'anthropic-prod': {
    preset: 'anthropic',
    apiKeyEnv: 'ANTHROPIC_API_KEY',
    secretName: 'anthropic-prod-api-key',
    contextWindowTokens: 200000,
  },
}

export async function ensureOrg(client: OmnaraClient, env: CliEnv, preferredOrgId?: string): Promise<string> {
  if (env.apiKeyKind === 'org') {
    if (env.orgId == null) {
      throw new Error('OMNARA_ORG_ID is required with an org API key; the key is bound to a single organization')
    }
    complete('org', env.orgId)
    return env.orgId
  }
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
  const preferred = orgs.find((org) => org.id === preferredOrgId)
  if (preferred != null) {
    complete('org', `${preferred.name} (${preferred.id})`)
    return preferred.id
  }
  if (orgs.length === 1) {
    const org = orgs[0]!
    complete('org', `${org.name} (${org.id})`)
    return org.id
  }
  if (orgs.length === 0) {
    progress('org', 'Creating...')
    const { data: created } = await sdk.createOrganization({
      client,
      headers: { 'Idempotency-Key': 'cli-agent-local-org' },
      body: { name: 'CLI Agent' },
    })
    complete('org', `${created.org.name} (${created.org.id})`)
    return created.org.id
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

export async function ensureMachine(client: OmnaraClient, orgId: string): Promise<EnsuredMachine> {
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

async function ensureOrgSecret(client: OmnaraClient, orgId: string, name: string, value: string): Promise<string> {
  const { data } = await sdk.listSecrets({
    client,
    path: { orgID: orgId },
    query: { owner_kind: 'org', name },
  })
  const match = data.data.find((secret) => secret.name === name)
  if (match != null) return match.id
  const { data: secret } = await sdk.createSecret({
    client,
    path: { orgID: orgId },
    headers: { 'Idempotency-Key': `cli-agent-secret-${name}` },
    body: {
      owner: { kind: 'org' },
      name,
      material: { kind: 'generic', value },
    },
  })
  return secret.id
}

export async function ensureModelAccess(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  profile: AgentProfileSource,
): Promise<void> {
  const providerConfigName = profile.model?.provider_config?.trim() ?? ''
  const modelName = profile.model?.name?.trim() ?? ''
  if (providerConfigName === '' || modelName === '') {
    throw new Error('agent-profile.yaml must set model.provider_config and model.name')
  }

  progress('model provider', 'Looking up...')
  const { data: configs } = await sdk.listModelProviderConfigs({ client, path: { orgID: orgId } })
  let providerConfig = configs.data.find((config) => config.name === providerConfigName)
  if (providerConfig == null) {
    const preset = modelProviderPresets[providerConfigName]
    if (preset == null) {
      throw new Error(
        `model provider config ${JSON.stringify(providerConfigName)} does not exist and has no local preset; ` +
          `create it through the API or use one of: ${Object.keys(modelProviderPresets).join(', ')}`,
      )
    }
    const apiKey = process.env[preset.apiKeyEnv]?.trim() ?? ''
    if (apiKey === '') {
      throw new Error(`agent-profile.yaml selects ${providerConfigName}; set ${preset.apiKeyEnv} to bootstrap it`)
    }
    progress('model provider', 'Creating credential secret...')
    const secretId = await ensureOrgSecret(client, orgId, preset.secretName, apiKey)
    progress('model provider', 'Creating...')
    const { data } = await sdk.createModelProviderConfig({
      client,
      path: { orgID: orgId },
      headers: { 'Idempotency-Key': `cli-agent-model-provider-${providerConfigName}` },
      body: { name: providerConfigName, preset: preset.preset, credential_secret_id: secretId },
    })
    providerConfig = data.config
  }
  complete('model provider', `${providerConfig.name} (${providerConfig.id})`)

  progress('model', 'Looking up...')
  const { data: models } = await sdk.listConfiguredModels({
    client,
    path: { orgID: orgId, modelProviderConfigID: providerConfig.id },
  })
  let model = models.data.find((candidate) => candidate.name === modelName)
  if (model == null) {
    const preset = modelProviderPresets[providerConfigName]
    const supportsReasoning = providerConfig.api_format !== 'anthropic-messages'
    progress('model', 'Creating...')
    const { data } = await sdk.createConfiguredModel({
      client,
      path: { orgID: orgId, modelProviderConfigID: providerConfig.id },
      headers: { 'Idempotency-Key': `cli-agent-model-${providerConfig.id}-${modelName}` },
      body: {
        name: modelName,
        provider_model_slug: modelName,
        context_window_tokens: preset?.contextWindowTokens ?? 128000,
        supports_tools: true,
        ...(supportsReasoning
          ? { supports_reasoning: true, supported_reasoning_efforts: reasoningEfforts }
          : {}),
      },
    })
    model = data
  }
  complete('model', `${model.name} (${model.id})`)

  progress('model grant', 'Checking...')
  let cursor: string | undefined
  do {
    const { data } = await sdk.listProjectModelGrants({
      client,
      path: { orgID: orgId, projectID: projectId },
      query: { cursor },
    })
    const match = data.data.find((item) => item.grant.configured_model_id === model.id)
    if (match != null) {
      complete('model grant', match.grant.id)
      return
    }
    cursor = data.next_cursor ?? undefined
  } while (cursor != null)
  progress('model grant', 'Creating...')
  const { data: granted } = await sdk.createProjectModelGrant({
    client,
    path: { orgID: orgId, projectID: projectId },
    headers: { 'Idempotency-Key': `cli-agent-model-grant-${model.id}` },
    body: { configured_model_id: model.id },
  })
  complete('model grant', granted.grant.id)
}

export async function loadAgentProfileSource(
  profilePath: string,
  machineName: string,
  cwd: string,
): Promise<{ profile: AgentProfileSource; source: string }> {
  const raw = await readFile(profilePath, 'utf8')
  const profile = YAML.parse(raw) as AgentProfileSource | null
  if (profile == null || typeof profile !== 'object') {
    throw new Error(`${profilePath} must contain an agent config object`)
  }
  if (profile.machine_sources == null) {
    profile.machine_sources = [{ machine_name: machineName, cwd }]
  }
  const instruction = typeof profile.instruction === 'string' ? profile.instruction.trimEnd() : ''
  profile.instruction = `${instruction}\nThe user's working directory on ${machineName} is ${cwd}.\n`
  return { profile, source: YAML.stringify(profile) }
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
  if (config.source == null || config.source_format !== 'yaml') {
    throw new Error(`agent config ${agentConfigId} has no yaml source to resume from`)
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
  source: string,
): Promise<{ profile: AgentProfile; config: AgentConfig }> {
  progress('agent config', 'Compiling...')
  const { data: config } = await sdk.createAgentConfig({
    client,
    path: { orgID: orgId, projectID: projectId },
    body: { source, source_format: 'yaml' },
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
    headers: { 'Idempotency-Key': `cli-agent-${new Date().toISOString()}` },
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
