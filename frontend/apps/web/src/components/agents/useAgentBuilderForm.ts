import type { ToolPermissionSelection } from '@omnara/sdk'
import { useEffect, useState } from 'react'

import type { BasicConfigSession } from '@/components/agents/agentConfigBasicYaml'
import type { ModelSelection } from '@/components/agents/AgentConfigModelField'
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

const emptyBasicConfig: BasicConfig = {
  instruction: '',
  providerConfig: '',
  modelName: '',
  machineSources: [],
  tools: [],
  mcpServers: [],
  skillIds: [],
}

export function useAgentBuilderForm(
  session: BasicConfigSession,
  onYamlChange: (yaml: string, blocked: boolean) => void,
) {
  const [draft, setDraft] = useState<BasicConfig>(session.initialDraft ?? emptyBasicConfig)
  const [unavailableSkillIds, setUnavailableSkillIds] = useState<string[]>([])
  const [unavailableSourceIds, setUnavailableSourceIds] = useState<string[]>([])
  const [modelUnavailable, setModelUnavailable] = useState(false)

  const blocked =
    unavailableSkillIds.length > 0 ||
    unavailableSourceIds.length > 0 ||
    modelUnavailable ||
    !draftValid(draft)

  useEffect(() => {
    onYamlChange(session.apply(draft), blocked)
  }, [blocked, draft, onYamlChange, session])

  const patch = (fields: Partial<BasicConfig>) => {
    setDraft((prev) => ({ ...prev, ...fields }))
  }

  return {
    instruction: draft.instruction,
    model: { providerConfig: draft.providerConfig, modelName: draft.modelName },
    machineSources: draft.machineSources,
    tools: draft.tools,
    skillIds: draft.skillIds,
    mcpServers: draft.mcpServers,
    setInstruction: (instruction: string) => {
      patch({ instruction })
    },
    setModel: (model: ModelSelection) => {
      patch({ providerConfig: model.providerConfig, modelName: model.modelName })
    },
    setMachineSources: (machineSources: BasicMachineSource[]) => {
      patch({ machineSources })
    },
    setTools: (tools: BasicTool[]) => {
      patch({ tools })
    },
    setSkillIds: (skillIds: string[]) => {
      patch({ skillIds })
    },
    setMcpServers: (mcpServers: BasicMcpServer[]) => {
      patch({ mcpServers })
    },
    reportModelUnavailable: setModelUnavailable,
    reportUnavailableSourceIds: setUnavailableSourceIds,
    reportUnavailableSkillIds: setUnavailableSkillIds,
  }
}

function draftValid(draft: BasicConfig) {
  return (
    draft.instruction.trim() !== '' &&
    draft.providerConfig.trim() !== '' &&
    draft.modelName.trim() !== '' &&
    draft.machineSources.every(machineSourceValid) &&
    mcpServerNamesUnique(draft.mcpServers) &&
    draft.mcpServers.every(mcpServerValid)
  )
}

const positiveIntegerPattern = /^[1-9][0-9]*$/

function machineCountValid(value: string) {
  return value === '' || positiveIntegerPattern.test(value)
}

function machineSourceValid(source: BasicMachineSource) {
  return (
    source.name.trim() !== '' &&
    envOverlayRowsValid(source.envRows) &&
    secretEnvOverlayRowsValid(source.secretEnvRows) &&
    (source.kind === 'machine' ||
      (machineCountValid(source.initialNumMachines) &&
        machineCountValid(source.maxMachines) &&
        optionalPositiveInt32Valid(source.machineCpu) &&
        optionalPositiveInt32Valid(source.machineMemoryMb)))
  )
}

const mcpServerNamePattern = /^[a-zA-Z][a-zA-Z0-9-]{0,31}$/

function mcpServerValid(server: BasicMcpServer) {
  return (
    mcpServerNamePattern.test(server.name.trim()) &&
    server.url.trim() !== '' &&
    (server.authType === 'none' ||
      (server.secretId.trim() !== '' &&
        (server.authType !== 'sigv4' ||
          (server.service.trim() !== '' && server.region.trim() !== ''))))
  )
}

function mcpServerNamesUnique(servers: BasicMcpServer[]) {
  const names = servers.map((server) => server.name.trim())
  return new Set(names).size === names.length
}
