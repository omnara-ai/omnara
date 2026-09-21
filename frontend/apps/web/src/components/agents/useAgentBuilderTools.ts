import { useAgentConfigTools } from '@omnara/react'
import { ApiError } from '@omnara/sdk'

import type { McpEntry } from '@/components/agents/agentConfigBasicExtract'
import { mcpWire } from '@/components/agents/agentConfigMcp'
import { subagentValid, subagentWire } from '@/components/agents/agentConfigSubagents'
import {
  type BasicConfig,
  type BasicMcpServer,
  toolWire,
} from '@/components/agents/useAgentBuilderForm'

export function agentBuilderToolsSource(source: BasicConfig): string {
  return JSON.stringify({
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
    subagents: Object.fromEntries(
      source.subagents.filter(subagentValid).map((row) => {
        const { type, profile } = subagentWire(row)
        return [row.key, { type, profile }]
      }),
    ),
  })
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
  source: BasicConfig,
  scope: { orgId: string; projectId: string },
) {
  const query = useAgentConfigTools(scope.orgId, scope.projectId, {
    source_format: 'json',
    source: agentBuilderToolsSource(source),
  })
  const error = query.error
  const detail =
    error instanceof ApiError
      ? error.issues.map((issue) => `${issue.path}: ${issue.message}`).join('; ') || error.message
      : ''
  return { ...query, errorMessage: `Couldn’t load other tools${detail ? ': ' + detail : '.'}` }
}
