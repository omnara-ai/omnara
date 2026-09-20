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
  deferred?: boolean | null
}

export interface BasicMcpServer {
  id: string
  name: string
  url: string
  permission: PermissionSelection | null
  defaultEnabled: boolean
  deferred?: boolean
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
  if (server.deferred) wire.deferred = true
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
  const tools = server.tools.filter(
    (tool) => tool.enabled != null || tool.permission != null || tool.deferred != null,
  )
  if (tools.length > 0) {
    wire.tools = Object.fromEntries(tools.map((tool) => [tool.name, mcpToolWire(tool)]))
  }
  return wire
}

function mcpToolWire(tool: BasicMcpTool): McpToolEntry {
  const wire: McpToolEntry = {}
  if (tool.enabled != null) wire.enabled = tool.enabled
  if (tool.permission != null) wire.permission = permissionWire(tool.permission)
  if (tool.deferred != null) wire.deferred = tool.deferred
  return wire
}

export function permissionWire(permission: PermissionSelection): PermissionEntry {
  return Object.keys(permission.parameters).length > 0
    ? { mode: permission.mode, parameters: permission.parameters }
    : { mode: permission.mode }
}

export const mcpServerNameMaxLength = 32

const mcpServerNamePattern = /^[a-zA-Z][a-zA-Z0-9-]{0,31}$/

export function mcpServerNameError(name: string): string | undefined {
  if (name === '') return 'Name is required.'
  if (name.length > mcpServerNameMaxLength) {
    return `Name cannot exceed ${mcpServerNameMaxLength} characters.`
  }
  if (!/^[a-zA-Z]/.test(name)) return 'Name must start with a letter.'
  if (!mcpServerNamePattern.test(name)) {
    return 'Name may only contain letters, numbers, and hyphens.'
  }
  return undefined
}

export const mcpRuntimeToolNameMaxLength = 64

export function mcpRuntimeToolName(serverName: string, toolName: string) {
  return `mcp__${serverName}__${toolName}`
}

export function mcpToolEnabled(server: BasicMcpServer, toolName: string) {
  return server.tools.find((tool) => tool.name === toolName)?.enabled ?? server.defaultEnabled
}

const mcpToolNamePattern = /^[a-zA-Z][a-zA-Z0-9_-]{0,63}$/

export function mcpToolNameAddable(toolName: string) {
  return mcpToolNamePattern.test(toolName)
}

export function mcpRuntimeToolNameError(serverName: string, toolName: string): string | undefined {
  if (toolName === '') return 'Tool name is required.'
  if (!/^[a-zA-Z]/.test(toolName)) {
    return `"${toolName}" must start with a letter, but the model only accepts tool names that begin with a letter.`
  }
  if (!/^[a-zA-Z0-9_-]*$/.test(toolName)) {
    return `"${toolName}" contains characters other than letters, numbers, underscores, and hyphens, which the model does not accept in tool names.`
  }
  const runtimeName = mcpRuntimeToolName(serverName, toolName)
  if (runtimeName.length <= mcpRuntimeToolNameMaxLength) return undefined
  const maxServerNameLength = mcpRuntimeToolNameMaxLength - mcpRuntimeToolName('', toolName).length
  const prefixed = `"${toolName}" becomes "${runtimeName}" (${runtimeName.length} characters) once the server name is prefixed, but the model only accepts tool names of ${mcpRuntimeToolNameMaxLength} characters or fewer.`
  return maxServerNameLength >= 1
    ? `${prefixed} Shorten the server name to ${maxServerNameLength} characters or fewer.`
    : `${prefixed} The tool name itself is too long to expose under any server name.`
}

export interface UnexposableMcpTool {
  name: string
  error: string
}

export function unexposableMcpTools(
  server: BasicMcpServer,
  discoveredNames: string[],
): UnexposableMcpTool[] {
  const names = new Set([...discoveredNames, ...server.tools.map((tool) => tool.name)])
  return [...names].flatMap((name) => {
    if (!mcpToolEnabled(server, name)) return []
    const error = mcpRuntimeToolNameError(server.name, name)
    return error === undefined ? [] : [{ name, error }]
  })
}
