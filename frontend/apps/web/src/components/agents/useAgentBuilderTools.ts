import { useAgentConfigTools } from '@omnara/react'
import { type AgentConfigToolsDefinition, ApiError } from '@omnara/sdk'
import {
  type ConfigSourceFormat,
  configYamlOptions,
  parseConfigDocument,
  serializeConfigDocument,
} from '@omnara/sdk/config-source'
import { useMemo } from 'react'
import { Document, isMap, isSeq } from 'yaml'

import type { McpEntry } from '@/components/agents/agentConfigBasicExtract'
import { toolWire } from '@/components/agents/agentConfigBasicWrite'
import { mcpWire } from '@/components/agents/agentConfigMcp'
import { subagentValid, subagentWire } from '@/components/agents/agentConfigSubagents'
import type { BasicConfig, BasicMcpServer } from '@/components/agents/useAgentBuilderForm'
import { formatIssue } from '@/lib/agent-config-issues'

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
>

export function agentBuilderToolsDefinition(source: ToolPreviewDraft): AgentConfigToolsDefinition {
  return {
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
}

export function agentBuilderToolsRequest(source: ToolPreviewDraft, format: ConfigSourceFormat) {
  const definition = agentBuilderToolsDefinition(source)
  const toolFields = new Set(Object.keys(definition))
  const advanced = (source.advancedEntries ?? []).filter(
    (entry) => !entry.removed && toolFields.has(entry.path[0] ?? ''),
  )
  const preview = new Document(definition, configYamlOptions)
  for (const entry of advanced) {
    const parsed = parseConfigDocument(entry.source, format)
    const fatal = parsed.diagnostics.find((issue) => issue.kind === 'fatal')
    if (fatal) throw new Error(`${entry.path.join(' → ')}: ${fatal.message}`)
    const value = parsed.doc.contents
    if (value == null) throw new Error(`${entry.path.join(' → ')}: Enter a definition.`)
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
  return { source: serializeConfigDocument(preview, format), source_format: format }
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
  }: ToolPreviewDraft,
  scope: { orgId: string; projectId: string },
  format: ConfigSourceFormat,
) {
  const request = useMemo(() => {
    try {
      return agentBuilderToolsRequest(
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
        },
        format,
      )
    } catch (error) {
      return error instanceof Error ? error : new Error(String(error))
    }
  }, [
    interactionHandlers,
    tools,
    mcpServers,
    machineSources,
    skillIds,
    memoryStores,
    subagents,
    gitCredentials,
    advancedEntries,
    format,
  ])
  const query = useAgentConfigTools(
    scope.orgId,
    scope.projectId,
    request instanceof Error ? null : request,
  )
  const error = request instanceof Error ? request : query.error
  const detail =
    error instanceof ApiError
      ? error.issues.map(formatIssue).join('; ') || error.message
      : (error?.message ?? '')
  return {
    ...query,
    isError: !!error,
    isPending: !(request instanceof Error) && query.isPending,
    errorMessage: `Couldn’t load tools${detail ? ': ' + detail : '.'}`,
  }
}
