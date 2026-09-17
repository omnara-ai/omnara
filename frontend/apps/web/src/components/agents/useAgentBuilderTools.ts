import { useAgentConfigTools } from '@omnara/react'

import { subagentValid, subagentWire } from '@/components/agents/agentConfigSubagents'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'

export function useAgentBuilderTools(
  source: BasicConfig,
  scope: { orgId: string; projectId: string },
) {
  return useAgentConfigTools(scope.orgId, scope.projectId, {
    source_format: 'json',
    source: JSON.stringify({
      tools: Object.fromEntries(source.tools.map((tool) => [tool.name, { enabled: tool.enabled }])),
      mcp: Object.fromEntries(
        source.mcpServers
          .filter((server) => server.name.trim() !== '' && server.url.trim() !== '')
          .map((server) => [server.name, { url: server.url }]),
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
    }),
  })
}
