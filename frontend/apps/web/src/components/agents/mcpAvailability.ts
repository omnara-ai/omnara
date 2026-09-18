import type { BasicMcpServer, BasicMcpTool } from '@/components/agents/agentConfigMcp'
import type { PermissionModeOption } from '@/components/agents/permissionModeOptions'
import {
  CircleIcon,
  EyeIcon,
  EyeSlashIcon,
  MagnifyingGlassCircleIcon,
} from '@/components/icons'

export type McpAvailability = 'enabled' | 'deferred' | 'disabled'
export type McpToolAvailability = 'inherit' | McpAvailability

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
    description: 'Hidden from the model until it finds the tool with tool_search.',
    icon: MagnifyingGlassCircleIcon,
  },
  {
    value: 'disabled',
    label: 'Disabled',
    description: 'Hidden from the agent.',
    icon: EyeSlashIcon,
  },
]

export const mcpToolAvailabilityOptions: readonly PermissionModeOption[] = [
  {
    value: 'inherit',
    label: 'Inherit',
    description: 'Follow the server default.',
    icon: CircleIcon,
  },
  ...mcpAvailabilityOptions,
]

const mcpAvailabilities: readonly McpAvailability[] = ['enabled', 'deferred', 'disabled']

export function parseMcpAvailability(value: string): McpAvailability | null {
  return mcpAvailabilities.find((candidate) => candidate === value) ?? null
}

export function parseMcpToolAvailability(value: string): McpToolAvailability | null {
  return value === 'inherit' ? 'inherit' : parseMcpAvailability(value)
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

export function mcpToolAvailability(tool: BasicMcpTool): McpToolAvailability {
  if (tool.enabled === false) return 'disabled'
  if (tool.deferred === true) return 'deferred'
  if (tool.enabled === true || tool.deferred === false) return 'enabled'
  return 'inherit'
}

export function mcpToolAvailabilityPatch(
  availability: McpToolAvailability,
): Pick<BasicMcpTool, 'enabled' | 'deferred'> {
  switch (availability) {
    case 'inherit':
      return { enabled: null, deferred: null }
    case 'enabled':
      return { enabled: true, deferred: false }
    case 'deferred':
      return { enabled: true, deferred: true }
    case 'disabled':
      return { enabled: false, deferred: null }
  }
}
