import type { ToolPermissionSelection } from '@omnara/sdk'

import type { BasicTool } from '@/components/agents/AgentConfigToolsField'
import {
  type EnvOverlayRow,
  envOverlayRowsValid,
  optionalPositiveInt32Valid,
  type ProviderOptionsDraft,
  type SecretEnvOverlayRow,
  secretEnvOverlayRowsValid,
} from '@/components/machines/machineOverrides'

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
  provider: string
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
