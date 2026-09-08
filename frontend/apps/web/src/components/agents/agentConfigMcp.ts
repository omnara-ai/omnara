import type {
  McpEntry,
  McpToolEntry,
  PermissionEntry,
  PermissionSelection,
} from '@/components/agents/agentConfigBasicExtract'

export type McpAuthType = 'none' | 'oauth' | 'bearer' | 'sigv4'

export interface BasicMcpTool {
  name: string
  enabled: boolean | null
  permission: PermissionSelection | null
}

export interface BasicMcpServer {
  id: string
  name: string
  url: string
  permission: PermissionSelection | null
  defaultEnabled: boolean
  authType: McpAuthType
  secretId: string
  service: string
  region: string
  tools: BasicMcpTool[]
}

export function mcpWire(server: BasicMcpServer): McpEntry {
  const wire: McpEntry = { url: server.url.trim() }
  if (server.permission != null) wire.permission = permissionWire(server.permission)
  wire.default_enabled = server.defaultEnabled
  if (server.authType !== 'none') {
    const secretId = server.secretId.trim()
    wire.auth =
      server.authType === 'sigv4'
        ? {
            type: 'sigv4',
            secret_id: secretId,
            service: server.service.trim(),
            region: server.region.trim(),
          }
        : { type: server.authType, secret_id: secretId }
  }
  const tools = server.tools.filter((tool) => tool.enabled != null || tool.permission != null)
  if (tools.length > 0) {
    wire.tools = Object.fromEntries(tools.map((tool) => [tool.name, mcpToolWire(tool)]))
  }
  return wire
}

function mcpToolWire(tool: BasicMcpTool): McpToolEntry {
  const wire: McpToolEntry = {}
  if (tool.enabled != null) wire.enabled = tool.enabled
  if (tool.permission != null) wire.permission = permissionWire(tool.permission)
  return wire
}

export function permissionWire(permission: PermissionSelection): PermissionEntry {
  return Object.keys(permission.parameters).length > 0
    ? { mode: permission.mode, parameters: permission.parameters }
    : { mode: permission.mode }
}
