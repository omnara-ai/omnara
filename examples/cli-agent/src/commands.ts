import type { OmnaraClient, ToolCatalog, ToolPermissionMode } from '@omnara/sdk'
import { sdk } from '@omnara/sdk'

import type { AgentProfileSource, AgentProfileToolSource } from './bootstrap.js'

export interface ConfigSession {
  client: OmnaraClient
  orgId: string
  projectId: string
  agentId: string
  profile: AgentProfileSource
  toolCatalog?: ToolCatalog
}

const permissionModes: Record<string, string> = {
  ask: 'always_ask',
  allow: 'always_allow',
}

export async function runConfigCommand(session: ConfigSession, line: string): Promise<string> {
  const [command = '', ...args] = line.split(/\s+/)
  switch (command) {
    case '/model':
      return setModel(session, args)
    case '/effort':
      return setEffort(session, args)
    case '/permission':
      return setPermission(session, args)
    default:
      throw new Error(`unknown command ${command}; available: /model, /effort, /mode, /permission, /quit`)
  }
}

async function setModel(session: ConfigSession, args: string[]): Promise<string> {
  const slug = args[0]
  if (slug == null || args.length !== 1) throw new Error('usage: /model <model-slug>')
  const profile = structuredClone(session.profile)
  profile.model = { ...profile.model, name: slug }
  await pushConfig(session, profile)
  return `model set to ${slug}`
}

async function setEffort(session: ConfigSession, args: string[]): Promise<string> {
  const effort = args[0]
  if (effort == null || args.length !== 1) throw new Error('usage: /effort <effort-level>')
  const profile = structuredClone(session.profile)
  profile.model = { ...profile.model, reasoning: { effort } }
  await pushConfig(session, profile)
  return `reasoning effort set to ${effort}`
}

async function setPermission(session: ConfigSession, args: string[]): Promise<string> {
  const mode = permissionModes[args[args.length - 1] ?? '']
  if (mode == null || args.length < 1 || args.length > 2) {
    throw new Error('usage: /permission [tool] <ask|allow>')
  }
  const profile = structuredClone(session.profile)
  const tools = profile.tools ?? {}
  const declared = Object.keys(tools).sort()
  if (declared.length === 0) throw new Error('the agent config declares no tools')
  const toolName = args.length === 2 ? args[0]! : undefined
  if (toolName != null && tools[toolName] == null) {
    throw new Error(`tool ${JSON.stringify(toolName)} is not declared in the agent config (declared: ${declared.join(', ')})`)
  }
  const catalog = await loadToolCatalog(session)
  const updated: string[] = []
  const skipped: string[] = []
  for (const name of toolName != null ? [toolName] : declared) {
    const supported = permissionModesFor(catalog, name, tools[name]!)
    if (!supported.some((candidate) => candidate.name === mode)) {
      skipped.push(name)
      continue
    }
    tools[name] = { ...tools[name], permission: { mode } }
    updated.push(name)
  }
  if (updated.length === 0) {
    throw new Error(
      toolName != null
        ? `tool ${JSON.stringify(toolName)} does not support ${mode}`
        : `no declared tool supports ${mode}`,
    )
  }
  profile.tools = tools
  await pushConfig(session, profile)
  const summary = `permission ${mode} set for ${updated.join(', ')}`
  return skipped.length > 0 ? `${summary} (unsupported, unchanged: ${skipped.join(', ')})` : summary
}

function permissionModesFor(
  catalog: ToolCatalog,
  name: string,
  tool: AgentProfileToolSource,
): ToolPermissionMode[] {
  if (tool.type === 'custom') return catalog.custom_tool_permissions.permission_modes
  return catalog.built_in_tools.find((entry) => entry.name === name)?.permission_modes ?? []
}

async function loadToolCatalog(session: ConfigSession): Promise<ToolCatalog> {
  if (session.toolCatalog == null) {
    const { data } = await sdk.getToolCatalog({ client: session.client })
    session.toolCatalog = data
  }
  return session.toolCatalog
}

async function pushConfig(session: ConfigSession, profile: AgentProfileSource): Promise<void> {
  await sdk.updateAgentConfig({
    client: session.client,
    path: { orgID: session.orgId, projectID: session.projectId, agentID: session.agentId },
    body: { source: JSON.stringify(profile), source_format: 'json' },
  })
  session.profile = profile
}
