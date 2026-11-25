import type { BasicMcpServer, BasicMcpTool } from '@/components/agents/agentConfigMcp'
import type { PermissionModeOption } from '@/components/agents/permissionModeOptions'
import { EyeIcon, EyeSlashIcon, MagnifyingGlassCircleIcon } from '@/components/icons'

export type McpAvailability = 'enabled' | 'deferred' | 'disabled'

export const mcpAvailabilityOptions: readonly PermissionModeOption[] = [
  {
    value: 'enabled',
    label: 'Enabled',
    description: 'Always in the model’s context.',
    icon: EyeIcon,
  },
  {
    value: 'deferred',
    label: 'Deferred',
    description: 'Hidden from the model’s context until it finds the tool with tool_search.',
    icon: MagnifyingGlassCircleIcon,
  },
  {
    value: 'disabled',
    label: 'Disabled',
    description: 'Hidden from the agent.',
    icon: EyeSlashIcon,
  },
]

const mcpAvailabilities: readonly McpAvailability[] = ['enabled', 'deferred', 'disabled']

export function parseMcpAvailability(value: string): McpAvailability | null {
  return mcpAvailabilities.find((candidate) => candidate === value) ?? null
}

export function mcpServerAvailability(server: BasicMcpServer): McpAvailability {
  if (!server.defaultEnabled) return 'disabled'
  return server.deferred ? 'deferred' : 'enabled'
}

export function mcpServerAvailabilityPatch(
  availability: McpAvailability,
): Pick<BasicMcpServer, 'defaultEnabled' | 'deferred'> {
  switch (availability) {
    case 'enabled':
      return { defaultEnabled: true, deferred: undefined }
    case 'deferred':
      return { defaultEnabled: true, deferred: true }
    case 'disabled':
      return { defaultEnabled: false, deferred: undefined }
  }
}

export function mcpToolAvailability(
  tool: Pick<BasicMcpTool, 'enabled' | 'deferred'> | undefined,
  serverAvailability: McpAvailability,
): McpAvailability {
  if (tool == null) return serverAvailability
  if (tool.enabled === false) return 'disabled'
  if (tool.enabled == null && serverAvailability === 'disabled') return 'disabled'
  if (tool.deferred === true) return 'deferred'
  if (tool.deferred === false) return 'enabled'
  return serverAvailability === 'deferred' ? 'deferred' : 'enabled'
}

export function mcpToolAvailabilityPatch(
  availability: McpAvailability,
  serverAvailability: McpAvailability,
): Pick<BasicMcpTool, 'enabled' | 'deferred'> {
  if (availability === serverAvailability) return { enabled: null, deferred: null }
  switch (availability) {
    case 'enabled':
      return { enabled: true, deferred: false }
    case 'deferred':
      return { enabled: true, deferred: true }
    case 'disabled':
      return { enabled: false, deferred: null }
  }
}
