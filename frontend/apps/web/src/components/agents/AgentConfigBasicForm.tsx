import { useToolCatalog } from '@omnara/react'

import type { BasicConfigSession } from '@/components/agents/agentConfigBasicYaml'
import { AgentConfigMachineSourcesField } from '@/components/agents/AgentConfigMachineSourcesField'
import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigModelField } from '@/components/agents/AgentConfigModelField'
import { AgentConfigSkillsField } from '@/components/agents/AgentConfigSkillsField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import { useAgentBuilderForm } from '@/components/agents/useAgentBuilderForm'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

export function AgentConfigBasicForm({
  orgId,
  projectId,
  session,
  onYamlChange,
}: {
  orgId: string
  projectId: string
  session: BasicConfigSession
  onYamlChange: (yaml: string, blocked: boolean) => void
}) {
  const form = useAgentBuilderForm(session, onYamlChange)
  const toolCatalog = useToolCatalog()

  return (
    <FieldGroup className="gap-8">
      <Field>
        <FieldLabel htmlFor="agent-config-basic-instruction">Agent Instructions</FieldLabel>
        <Textarea
          id="agent-config-basic-instruction"
          value={form.instruction}
          placeholder="You are a research assistant. When given a topic, gather sources and produce a short summary with citations."
          className="min-h-36 resize-y"
          onChange={(event) => {
            form.setInstruction(event.target.value)
          }}
        />
      </Field>
      <AgentConfigModelField
        orgId={orgId}
        projectId={projectId}
        value={form.model}
        onChange={form.setModel}
        onUnavailableChange={form.reportModelUnavailable}
      />
      <AgentConfigMachineSourcesField
        orgId={orgId}
        projectId={projectId}
        sources={form.machineSources}
        onSourcesChange={form.setMachineSources}
        onUnavailableIdsChange={form.reportUnavailableSourceIds}
      />
      <AgentConfigToolsField
        catalog={toolCatalog.data}
        tools={form.tools}
        onToolsChange={form.setTools}
      />
      <AgentConfigSkillsField
        orgId={orgId}
        projectId={projectId}
        selectedIds={form.skillIds}
        onSelectedIdsChange={form.setSkillIds}
        onUnavailableIdsChange={form.reportUnavailableSkillIds}
      />
      <AgentConfigMcpServersField
        orgId={orgId}
        projectId={projectId}
        permissionProfile={toolCatalog.data?.mcp_tool_permissions}
        servers={form.mcpServers}
        onServersChange={form.setMcpServers}
      />
    </FieldGroup>
  )
}
