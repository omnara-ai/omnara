import { useAgentConfigTools } from '@omnara/react'
import type { ResolvedAgentConfigTool } from '@omnara/sdk'
import { useState } from 'react'

import { subagentValid, subagentWire } from '@/components/agents/agentConfigSubagents'
import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'

type ToolSourceContext = Pick<BasicConfig, 'machineSources' | 'skillIds' | 'subagents'>
const emptySources: ToolSourceContext = { machineSources: [], skillIds: [], subagents: [] }

export function useAgentBuilderTools(
  source: ToolSourceContext,
  scope: { orgId: string; projectId: string },
) {
  const [removedSources, setRemovedSources] = useState<ToolSourceContext | null>(null)
  const current = useAgentConfigTools(scope.orgId, scope.projectId, defaultsRequest(source))
  const previous = useAgentConfigTools(
    removedSources == null ? '' : scope.orgId,
    scope.projectId,
    defaultsRequest(removedSources ?? emptySources),
  )
  return {
    pending: current.isPending || (removedSources != null && previous.isPending),
    error: current.isError || (removedSources != null && previous.isError),
    normalize: (tools: BasicTool[]) =>
      source.subagents.every(subagentValid)
        ? normalizeTools(tools, current.data?.tools, previous.data?.tools ?? [])
        : tools,
    sourcesChanged: () => {
      setRemovedSources((old) => ({
        machineSources: [
          ...new Map(
            [...(old?.machineSources ?? []), ...source.machineSources].map((row) => [
              `${row.kind}:${row.name}`,
              row,
            ]),
          ).values(),
        ],
        skillIds: [...new Set([...(old?.skillIds ?? []), ...source.skillIds])],
        subagents: [
          ...new Map(
            [...(old?.subagents ?? []), ...source.subagents]
              .filter(subagentValid)
              .map((row) => [row.key, row]),
          ).values(),
        ],
      }))
    },
    reset: () => {
      setRemovedSources(null)
    },
    retry: () => {
      void current.refetch()
      if (removedSources != null) void previous.refetch()
    },
  }
}

function defaultsRequest(source: ToolSourceContext) {
  return {
    source_format: 'json' as const,
    source: JSON.stringify({
      machine_sources: source.machineSources
        .filter((row) => row.name.trim() !== '')
        .map((row) =>
          row.kind === 'pool' ? { machine_pool_name: row.name } : { machine_name: row.name },
        ),
      skills: source.skillIds,
      subagents: Object.fromEntries(
        source.subagents.filter(subagentValid).map((row) => [row.key, subagentWire(row)]),
      ),
    }),
  }
}

function normalizeTools(
  tools: BasicTool[],
  defaults: ResolvedAgentConfigTool[] | undefined,
  removed: ResolvedAgentConfigTool[],
): BasicTool[] {
  if (defaults === undefined) return tools
  const required = new Set(defaults.map((tool) => tool.name))
  const removedNames = new Set(removed.map((tool) => tool.name))
  const result = tools.filter((tool) => !removedNames.has(tool.name) || required.has(tool.name))
  const configured = new Set(result.map((tool) => tool.name))
  for (const tool of defaults) {
    if (!configured.has(tool.name)) {
      result.push({
        name: tool.name,
        enabled: tool.enabled,
        permission: null,
      })
    }
  }
  return result
}
