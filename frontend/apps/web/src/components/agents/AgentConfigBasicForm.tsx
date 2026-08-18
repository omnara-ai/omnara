import { useToolCatalog } from '@omnara/react'

import { type BasicConfigDraft } from '@/components/agents/agentConfigBasicSerialization'
import { AgentConfigMachineSourcesField } from '@/components/agents/AgentConfigMachineSourcesField'
import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigModelField } from '@/components/agents/AgentConfigModelField'
import { AgentConfigSkillsField } from '@/components/agents/AgentConfigSkillsField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

export function AgentConfigBasicForm({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  projectId: string
  value: BasicConfigDraft
  onChange: (draft: BasicConfigDraft) => void
}) {
  const toolCatalog = useToolCatalog()
  const { instruction, machineSources, mcpServers, modelName, providerConfig, skillIds, tools } =
    value

  return (
    <FieldGroup className="gap-8">
      <Field>
        <FieldLabel htmlFor="agent-config-basic-instruction" required>
          Instruction
        </FieldLabel>
        <FieldDescription>The system prompt that defines how this agent works.</FieldDescription>
        <Textarea
          id="agent-config-basic-instruction"
          aria-required
          value={instruction}
          placeholder="You are a research assistant. When given a topic, gather sources and produce a short summary with citations."
          className="min-h-36 resize-y"
          onChange={(event) => {
            onChange({ ...value, instruction: event.target.value })
          }}
        />
      </Field>
      <AgentConfigModelField
        orgId={orgId}
        projectId={projectId}
        value={{ providerConfig, modelName }}
        onChange={(selection) => {
          onChange({
            ...value,
            providerConfig: selection.providerConfig,
            modelName: selection.modelName,
          })
        }}
      />
      <AgentConfigMachineSourcesField
        orgId={orgId}
        projectId={projectId}
        sources={machineSources}
        onSourcesChange={(sources) => {
          onChange({ ...value, machineSources: sources })
        }}
      />
      <AgentConfigToolsField
        catalog={toolCatalog.data}
        tools={tools}
        onToolsChange={(nextTools) => {
          onChange({ ...value, tools: nextTools })
        }}
      />
      <AgentConfigSkillsField
        orgId={orgId}
        projectId={projectId}
        selectedIds={skillIds}
        onSelectedIdsChange={(nextSkillIds) => {
          onChange({ ...value, skillIds: nextSkillIds })
        }}
      />
      <AgentConfigMcpServersField
        orgId={orgId}
        projectId={projectId}
        permissionProfile={toolCatalog.data?.mcp_tool_permissions}
        servers={mcpServers}
        onServersChange={(servers) => {
          onChange({ ...value, mcpServers: servers })
        }}
      />
    </FieldGroup>
  )
}
