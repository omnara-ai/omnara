import type { ToolPermissionSelection } from '@omnara/sdk'

import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import {
  emptyProviderOptions,
  envFromRows,
  type EnvOverlayRow,
  envOverlayRowsValid,
  optionalPositiveInt32Valid,
  type ProviderOptionsDraft,
  providerOptionsOverlay,
  secretEnvFromRows,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
} from '@/components/machines/machineOverrides'
import { isMachinePoolProvider } from '@/components/org/machinePoolProviders'

export type McpAuthType = 'none' | 'oauth' | 'bearer' | 'sigv4'

export interface BasicMcpServer {
  id: string
  name: string
  url: string
  permission: ToolPermissionSelection
  defaultEnabled: boolean
  authType: McpAuthType
  secretId: string
  service: string
  region: string
}

export type MachineSourceKind = 'pool' | 'machine'

export interface BasicMachineSource {
  id: string
  kind: MachineSourceKind
  name: string
  /** Provider of the selected pool; '' until a pool is picked. */
  provider: string
  /** Management kind of the selected pool; '' until a pool is picked. */
  managementKind: string
  defaultCwd: string
  initialNumMachines: string
  maxMachines: string
  machineCpu: string
  machineMemoryMb: string
  providerOptions: ProviderOptionsDraft
  envRows: EnvOverlayRow[]
  secretEnvRows: SecretEnvOverlayRow[]
}

export interface BasicConfig {
  instruction: string
  providerConfig: string
  modelName: string
  machineSources: BasicMachineSource[]
  tools: BasicTool[]
  mcpServers: BasicMcpServer[]
  skillIds: string[]
}

export const emptyBasicConfig: BasicConfig = {
  instruction: '',
  providerConfig: '',
  modelName: '',
  machineSources: [],
  tools: [],
  mcpServers: [],
  skillIds: [],
}

export function newMachineSource(kind: MachineSourceKind): BasicMachineSource {
  return {
    id: crypto.randomUUID(),
    kind,
    name: '',
    provider: '',
    managementKind: '',
    defaultCwd: '',
    initialNumMachines: '',
    maxMachines: '',
    machineCpu: '',
    machineMemoryMb: '',
    providerOptions: emptyProviderOptions,
    envRows: [],
    secretEnvRows: [],
  }
}

const mcpServerNamePattern = /^[a-zA-Z][a-zA-Z0-9-]{0,31}$/
const positiveIntegerPattern = /^[1-9][0-9]*$/

function isMachineCountValid(value: string) {
  return value === '' || positiveIntegerPattern.test(value)
}

export function isBasicConfigComplete(config: BasicConfig) {
  const mcpServerNames = config.mcpServers.map((server) => server.name.trim())
  return (
    config.instruction.trim() !== '' &&
    config.providerConfig.trim() !== '' &&
    config.modelName.trim() !== '' &&
    config.machineSources.every(
      (source) =>
        source.name.trim() !== '' &&
        envOverlayRowsValid(source.envRows) &&
        secretEnvOverlayRowsValid(source.secretEnvRows) &&
        (source.kind === 'machine' ||
          (isMachineCountValid(source.initialNumMachines) &&
            isMachineCountValid(source.maxMachines) &&
            optionalPositiveInt32Valid(source.machineCpu) &&
            optionalPositiveInt32Valid(source.machineMemoryMb))),
    ) &&
    new Set(mcpServerNames).size === mcpServerNames.length &&
    config.mcpServers.every((server) => {
      const serverName = server.name.trim()
      return (
        mcpServerNamePattern.test(serverName) &&
        server.url.trim() !== '' &&
        (server.authType === 'none' ||
          (server.secretId.trim() !== '' &&
            (server.authType !== 'sigv4' ||
              (server.service.trim() !== '' && server.region.trim() !== ''))))
      )
    })
  )
}

function yamlString(value: string) {
  return JSON.stringify(value)
}

function yamlBlock(value: string) {
  const normalized = value.replace(/\r\n?/g, '\n').trimEnd()
  if (normalized === '') return '""'

  return `|\n${normalized
    .split('\n')
    .map((line) => `  ${line}`)
    .join('\n')}`
}

export function serializeBasicConfig(config: BasicConfig) {
  const lines: string[] = []
  lines.push(`instruction: ${yamlBlock(config.instruction.trimEnd())}`)
  lines.push('model:')
  lines.push(`  provider_config: ${yamlString(config.providerConfig.trim())}`)
  lines.push(`  name: ${yamlString(config.modelName.trim())}`)

  if (config.machineSources.length > 0) {
    lines.push('machine_sources:')
    for (const source of config.machineSources) {
      if (source.kind === 'pool') {
        lines.push(`  - machine_pool_name: ${yamlString(source.name.trim())}`)
        if (source.initialNumMachines !== '') {
          lines.push(`    initial_num_machines: ${source.initialNumMachines}`)
        }
        if (source.maxMachines !== '') {
          lines.push(`    max_machines: ${source.maxMachines}`)
        }
        if (source.machineCpu !== '') {
          lines.push(`    machine_cpu: ${source.machineCpu}`)
        }
        if (source.machineMemoryMb !== '') {
          lines.push(`    machine_memory_mb: ${source.machineMemoryMb}`)
        }
        const optionsOverlay = isMachinePoolProvider(source.provider)
          ? providerOptionsOverlay(
              source.provider,
              source.providerOptions,
              source.managementKind === 'cluster',
            )
          : undefined
        if (optionsOverlay) {
          lines.push(`    machine_provider_options_overlay: ${JSON.stringify(optionsOverlay)}`)
        }
      } else {
        lines.push(`  - machine_name: ${yamlString(source.name.trim())}`)
      }
      if (source.defaultCwd.trim() !== '') {
        lines.push(`    cwd: ${yamlString(source.defaultCwd.trim())}`)
      }
      const envOverlay = envFromRows(source.envRows)
      if (envOverlay) {
        lines.push(`    env_overlay: ${JSON.stringify(envOverlay)}`)
      }
      const secretEnvOverlay = secretEnvFromRows(source.secretEnvRows)
      if (secretEnvOverlay) {
        lines.push(`    secret_env_overlay: ${JSON.stringify(secretEnvOverlay)}`)
      }
    }
  }

  if (config.tools.length > 0) {
    lines.push('tools:')
    for (const tool of config.tools) {
      lines.push(`  ${tool.name}:`)
      lines.push('    type: built_in')
      appendPermission(lines, '    ', tool.permission)
    }
  }

  if (config.skillIds.length > 0) {
    lines.push('skills:')
    for (const skillId of config.skillIds) {
      lines.push(`  - ${yamlString(skillId)}`)
    }
  }

  if (config.mcpServers.length > 0) {
    lines.push('mcp:')
    for (const server of config.mcpServers) {
      lines.push(`  ${server.name.trim()}:`)
      lines.push(`    url: ${yamlString(server.url.trim())}`)
      appendPermission(lines, '    ', server.permission)
      lines.push(`    default_enabled: ${server.defaultEnabled ? 'true' : 'false'}`)
      if (server.authType !== 'none') {
        lines.push('    auth:')
        lines.push(`      type: ${server.authType}`)
        lines.push(`      secret_id: ${yamlString(server.secretId.trim())}`)
        if (server.authType === 'sigv4') {
          lines.push(`      service: ${yamlString(server.service.trim())}`)
          lines.push(`      region: ${yamlString(server.region.trim())}`)
        }
      }
    }
  }

  return `${lines.join('\n')}\n`
}

function appendPermission(lines: string[], indent: string, permission: ToolPermissionSelection) {
  lines.push(`${indent}permission:`)
  lines.push(`${indent}  mode: ${permission.mode}`)
  if (Object.keys(permission.parameters).length > 0) {
    lines.push(`${indent}  parameters: ${JSON.stringify(permission.parameters)}`)
  }
}
