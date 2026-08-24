import { z } from 'zod'

import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'

const STORAGE_KEY = 'omnara:pending-mcp-builder-oauth'

const permission = z.object({
  mode: z.string(),
  parameters: z.record(z.string(), z.unknown()),
})

const basicConfig: z.ZodType<BasicConfig> = z.object({
  instruction: z.string(),
  providerConfig: z.string(),
  modelName: z.string(),
  machineSources: z.array(
    z.object({
      id: z.string(),
      kind: z.enum(['pool', 'machine']),
      name: z.string(),
      provider: z.string(),
      managementKind: z.string(),
      defaultCwd: z.string(),
      initialNumMachines: z.string(),
      maxMachines: z.string(),
      deleteAfterIdleMinutes: z.string(),
      machineCpu: z.string(),
      machineMemoryGb: z.string(),
      providerOptions: z.object({
        resource: z.string(),
        location: z.string(),
        startupScript: z.string(),
      }),
      envRows: z.array(z.object({ id: z.string(), key: z.string(), value: z.string().nullable() })),
      secretEnvRows: z.array(
        z.object({ id: z.string(), key: z.string(), secretId: z.string().nullable() }),
      ),
    }),
  ),
  tools: z.array(z.object({ name: z.string(), permission: permission.nullable() })),
  mcpServers: z.array(
    z.object({
      id: z.string(),
      name: z.string(),
      url: z.string(),
      permission: permission.nullable(),
      defaultEnabled: z.boolean(),
      authType: z.enum(['none', 'oauth', 'bearer', 'sigv4']),
      secretId: z.string(),
      service: z.string(),
      region: z.string(),
    }),
  ),
  skillIds: z.array(z.string()),
})

const pendingRecord = z.object({
  returnPath: z.string(),
  serverId: z.string(),
  agentName: z.string().nullable(),
  draft: basicConfig,
})

export interface PendingMcpBuilderOAuth {
  returnPath: string
  serverId: string
  agentName: string | null
  draft: BasicConfig
}

export interface McpBuilderOAuthRestore {
  draft: BasicConfig
  agentName: string | null
}

export function savePendingMcpBuilderOAuth(record: PendingMcpBuilderOAuth) {
  window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(record))
}

export function hasPendingMcpBuilderOAuthOutcome() {
  return (
    outcomeFromSearch(new URLSearchParams(window.location.search)) !== null &&
    window.sessionStorage.getItem(STORAGE_KEY) !== null
  )
}

let consumed: { search: string; restore: McpBuilderOAuthRestore | null } | null = null

export function takeMcpBuilderOAuthRestore(): McpBuilderOAuthRestore | null {
  const search = window.location.search
  if (consumed?.search === search) return consumed.restore
  const restore = readRestore(search)
  consumed = { search, restore }
  return restore
}

function readRestore(search: string): McpBuilderOAuthRestore | null {
  const outcome = outcomeFromSearch(new URLSearchParams(search))
  if (outcome === null) return null
  const stored = window.sessionStorage.getItem(STORAGE_KEY)
  if (stored === null) return null
  window.sessionStorage.removeItem(STORAGE_KEY)
  const record = parseRecord(stored)
  if (record?.returnPath !== window.location.pathname) return null
  if (outcome.kind === 'error') {
    return { draft: record.draft, agentName: record.agentName }
  }
  return {
    agentName: record.agentName,
    draft: {
      ...record.draft,
      mcpServers: record.draft.mcpServers.map((server) =>
        server.id === record.serverId ? { ...server, secretId: outcome.secretId } : server,
      ),
    },
  }
}

function outcomeFromSearch(
  search: URLSearchParams,
): { kind: 'success'; secretId: string } | { kind: 'error' } | null {
  if (search.get('mcp_oauth_error') !== null) return { kind: 'error' }
  const secretId = search.get('secret_id')
  return search.get('mcp_oauth') === 'success' && secretId ? { kind: 'success', secretId } : null
}

function parseRecord(stored: string): PendingMcpBuilderOAuth | null {
  try {
    const parsed = pendingRecord.safeParse(JSON.parse(stored))
    return parsed.success ? parsed.data : null
  } catch {
    return null
  }
}
