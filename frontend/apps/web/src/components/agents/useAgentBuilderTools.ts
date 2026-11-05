import { useAgentConfigTools } from '@omnara/react'
import { type AgentConfigToolsDefinition, ApiError } from '@omnara/sdk'
import { useMemo } from 'react'
import { Document, isMap, isSeq, parseDocument } from 'yaml'

import type { McpEntry } from '@/components/agents/agentConfigBasicExtract'
import { toolWire } from '@/components/agents/agentConfigBasicWrite'
import { parseConfigDocument } from '@/components/agents/agentConfigCompatibility'
import { mcpWire } from '@/components/agents/agentConfigMcp'
import { subagentValid, subagentWire } from '@/components/agents/agentConfigSubagents'
import { configYamlOptions } from '@/components/agents/agentConfigYaml'
import type { BasicConfig, BasicMcpServer } from '@/components/agents/useAgentBuilderForm'

type ToolPreviewDraft = Pick<
  BasicConfig,
  | 'interactionHandlers'
  | 'tools'
  | 'mcpServers'
  | 'machineSources'
  | 'skillIds'
  | 'memoryStores'
  | 'subagents'
  | 'gitCredentials'
  | 'advancedEntries'
  | 'originalSource'
>

export function agentBuilderToolsDefinition(source: ToolPreviewDraft): AgentConfigToolsDefinition {
  const definition: AgentConfigToolsDefinition = {
    interaction_handlers: source.interactionHandlers,
    tools: Object.fromEntries(source.tools.map((tool) => [tool.name, toolWire(tool)])),
    mcp: Object.fromEntries(
      source.mcpServers
        .filter((server) => server.name.trim() !== '' && server.url.trim() !== '')
        .map((server) => [server.name, mcpPreviewWire(server)]),
    ),
    machine_sources: source.machineSources
      .filter((row) => row.name.trim() !== '')
      .map((row) =>
        row.kind === 'pool' ? { machine_pool_name: row.name } : { machine_name: row.name },
      ),
    skills: source.skillIds,
    memory_stores: source.memoryStores,
    git_credentials: source.gitCredentials,
    subagents: Object.fromEntries(
      source.subagents.filter(subagentValid).map((row) => {
        const { type, profile } = subagentWire(row)
        return [row.key, { type, profile }]
      }),
    ),
  }
  if (source.gitCredentials !== undefined) definition.git_credentials = source.gitCredentials
  return definition
}

export function agentBuilderToolsRequest(source: ToolPreviewDraft) {
  const definition = agentBuilderToolsDefinition(source)
  const toolFields = new Set([...Object.keys(definition), 'git_credentials'])
  const advanced = (source.advancedEntries ?? []).filter(
    (entry) => !entry.removed && toolFields.has(entry.path[0] ?? ''),
  )
  if (advanced.length === 0) return { source: definition, source_format: 'json' as const }
  const preview = new Document(definition, configYamlOptions)
  for (const entry of advanced) {
    const parsed = parseConfigDocument(entry.source)
    const value = !parsed.diagnostics.some((issue) => issue.kind === 'fatal')
      ? parsed.doc.contents
      : parseDocument(source.originalSource ?? '', configYamlOptions).getIn(entry.path, true)
    if (value == null) continue
    if (entry.path[0] === 'machine_sources' && entry.path.length === 2) {
      const sources = preview.get('machine_sources', true)
      if (isSeq(sources)) sources.add(value)
    } else {
      preview.setIn(entry.path, value)
    }
  }
  for (const key of ['mcp', 'subagents']) {
    const entries = preview.get(key, true)
    if (!isMap(entries)) continue
    for (const entry of entries.items) {
      if (!isMap(entry.value)) continue
      for (const field of key === 'mcp' ? ['auth'] : ['instruction', 'description', 'model'])
        entry.value.delete(field)
    }
  }
  return { source: preview.toString(), source_format: 'yaml' as const }
}

function mcpPreviewWire(server: BasicMcpServer): McpEntry {
  const wire = mcpWire(server)
  const preview: McpEntry = { url: wire.url, default_enabled: wire.default_enabled }
  if (wire.permission != null) preview.permission = wire.permission
  if (wire.deferred) preview.deferred = true
  if (wire.tools != null) preview.tools = wire.tools
  return preview
}

export function useAgentBuilderTools(
  {
    interactionHandlers,
    tools,
    mcpServers,
    machineSources,
    skillIds,
    memoryStores,
    subagents,
    gitCredentials,
    advancedEntries,
    originalSource,
  }: ToolPreviewDraft,
  scope: { orgId: string; projectId: string },
) {
  const request = useMemo(
    () =>
      agentBuilderToolsRequest({
        interactionHandlers,
        tools,
        mcpServers,
        machineSources,
        skillIds,
        memoryStores,
        subagents,
        gitCredentials,
        advancedEntries,
        originalSource,
      }),
    [
      interactionHandlers,
      tools,
      mcpServers,
      machineSources,
      skillIds,
      memoryStores,
      subagents,
      gitCredentials,
      advancedEntries,
      originalSource,
    ],
  )
  const query = useAgentConfigTools(scope.orgId, scope.projectId, request)
  const error = query.error
  const detail =
    error instanceof ApiError
      ? error.issues.map((issue) => `${issue.path}: ${issue.message}`).join('; ') || error.message
      : ''
  return { ...query, errorMessage: `Couldn’t load built-in tools${detail ? ': ' + detail : '.'}` }
}
