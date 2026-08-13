import { useToolCatalog } from '@omnara/react'
import { useCallback, useEffect, useState } from 'react'

import {
  type BasicConfig,
  type BasicMachineSource,
  type BasicMcpServer,
  isBasicConfigComplete,
} from '@/components/agents/agentConfigBasic'
import { applyBasicConfig } from '@/components/agents/agentConfigBasicYaml'
import { AgentConfigMachineSourcesField } from '@/components/agents/AgentConfigMachineSourcesField'
import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import {
  AgentConfigModelField,
  type ModelSelection,
} from '@/components/agents/AgentConfigModelField'
import { AgentConfigSkillsField } from '@/components/agents/AgentConfigSkillsField'
import { AgentConfigToolsField, type BasicTool } from '@/components/agents/AgentConfigToolsField'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

interface BasicConfigDraft {
  instruction: string
  providerConfig: string
  modelName: string
  machineSources: BasicMachineSource[]
  tools: BasicTool[]
  mcpServers: BasicMcpServer[]
  skillIds: string[]
}

const emptyDraft: BasicConfigDraft = {
  instruction: '',
  providerConfig: '',
  modelName: '',
  machineSources: [],
  tools: [],
  mcpServers: [],
  skillIds: [],
}

export function AgentConfigBasicForm({
  orgId,
  projectId,
  initialConfig,
  baselineSource = '',
  onYamlChange,
}: {
  orgId: string
  projectId: string
  initialConfig?: BasicConfig
  /** YAML source the initial config came from; edits are applied onto it so
   *  comments and formatting outside the changed fields survive. */
  baselineSource?: string
  /** Reports the draft's YAML on every change. `blocked` means the draft must
   *  not be saved yet: it is incomplete or references unavailable resources. */
  onYamlChange: (yaml: string, blocked: boolean) => void
}) {
  const [draft, setDraft] = useState<BasicConfigDraft>(initialConfig ?? emptyDraft)
  const [unavailableSkillIds, setUnavailableSkillIds] = useState<string[]>([])
  const [unavailableSourceIds, setUnavailableSourceIds] = useState<string[]>([])
  const [modelUnavailable, setModelUnavailable] = useState(false)
  const toolCatalog = useToolCatalog()
  const { instruction, machineSources, mcpServers, modelName, providerConfig, skillIds, tools } =
    draft

  useEffect(() => {
    const config = {
      instruction,
      providerConfig,
      modelName,
      machineSources,
      tools,
      mcpServers,
      skillIds,
    }
    const blocked =
      unavailableSkillIds.length > 0 ||
      unavailableSourceIds.length > 0 ||
      modelUnavailable ||
      !isBasicConfigComplete(config)
    onYamlChange(applyBasicConfig(baselineSource, config), blocked)
  }, [
    baselineSource,
    instruction,
    machineSources,
    mcpServers,
    modelName,
    modelUnavailable,
    onYamlChange,
    providerConfig,
    skillIds,
    tools,
    unavailableSkillIds.length,
    unavailableSourceIds.length,
  ])

  const onModelChange = useCallback((selection: ModelSelection) => {
    setDraft((prev) => ({
      ...prev,
      providerConfig: selection.providerConfig,
      modelName: selection.modelName,
    }))
  }, [])

  return (
    <FieldGroup className="gap-8">
      <Field>
        <FieldLabel htmlFor="agent-config-basic-instruction">Agent Instructions</FieldLabel>
        <Textarea
          id="agent-config-basic-instruction"
          value={instruction}
          placeholder="You are a research assistant. When given a topic, gather sources and produce a short summary with citations."
          className="min-h-36 resize-y"
          onChange={(event) => {
            setDraft((prev) => ({ ...prev, instruction: event.target.value }))
          }}
        />
      </Field>
      <AgentConfigModelField
        orgId={orgId}
        projectId={projectId}
        value={{ providerConfig, modelName }}
        onChange={onModelChange}
        onUnavailableChange={setModelUnavailable}
      />
      <AgentConfigMachineSourcesField
        orgId={orgId}
        projectId={projectId}
        sources={machineSources}
        onSourcesChange={(sources) => {
          setDraft((prev) => ({ ...prev, machineSources: sources }))
        }}
        onUnavailableIdsChange={setUnavailableSourceIds}
      />
      <AgentConfigToolsField
        catalog={toolCatalog.data}
        tools={tools}
        onToolsChange={(nextTools) => {
          setDraft((prev) => ({ ...prev, tools: nextTools }))
        }}
      />
      <AgentConfigSkillsField
        orgId={orgId}
        projectId={projectId}
        selectedIds={skillIds}
        onSelectedIdsChange={(nextSkillIds) => {
          setDraft((prev) => ({ ...prev, skillIds: nextSkillIds }))
        }}
        onUnavailableIdsChange={setUnavailableSkillIds}
      />
      <AgentConfigMcpServersField
        orgId={orgId}
        projectId={projectId}
        permissionProfile={toolCatalog.data?.mcp_tool_permissions}
        servers={mcpServers}
        onServersChange={(servers) => {
          setDraft((prev) => ({ ...prev, mcpServers: servers }))
        }}
      />
    </FieldGroup>
  )
}
