import { useToolCatalog } from '@omnara/react'

import { AgentConfigMachineSourcesField } from '@/components/agents/AgentConfigMachineSourcesField'
import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigSkillsField } from '@/components/agents/AgentConfigSkillsField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import { addMissingMachineTools, hasMissingMachineTools } from '@/components/agents/builtInTools'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'
import { Field, FieldGroup, RequiredFieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

export function AgentConfigBasicForm({
  orgId,
  projectId,
  form,
}: {
  orgId: string
  projectId: string
  form: AgentBuilderForm
}) {
  const toolCatalog = useToolCatalog()
  const showMissingMachineTools =
    form.machineSources.some((source) => source.name.trim() !== '') &&
    hasMissingMachineTools(form.tools)

  return (
    <FieldGroup className="gap-8">
      <Field>
        <RequiredFieldLabel htmlFor="agent-config-basic-instruction">
          Agent Instructions
        </RequiredFieldLabel>
        <Textarea
          id="agent-config-basic-instruction"
          required
          value={form.instruction}
          placeholder="You are a research assistant. When given a topic, gather sources and produce a short summary with citations."
          className="max-h-96 min-h-20 resize-y"
          onChange={(event) => {
            form.setInstruction(event.target.value)
          }}
        />
      </Field>
      <div className="space-y-2">
        <p className="text-muted-foreground text-right text-sm">Optional</p>
        <div className="bg-muted/15 divide-y overflow-hidden rounded-xl">
          <div className="p-5">
            <AgentConfigMachineSourcesField
              orgId={orgId}
              projectId={projectId}
              sources={form.machineSources}
              onSourcesChange={form.setMachineSources}
              onUnavailableIdsChange={form.reportUnavailableSourceIds}
              showMissingToolsWarning={showMissingMachineTools}
              onAddMissingTools={() => {
                form.setTools(addMissingMachineTools(form.tools))
              }}
            />
          </div>
          <div className="p-5">
            <AgentConfigToolsField
              catalog={toolCatalog.data}
              tools={form.tools}
              onToolsChange={form.setTools}
            />
          </div>
          <div className="p-5">
            <AgentConfigSkillsField
              orgId={orgId}
              projectId={projectId}
              selectedIds={form.skillIds}
              onSelectedIdsChange={form.setSkillIds}
              onUnavailableIdsChange={form.reportUnavailableSkillIds}
            />
          </div>
          <div className="p-5">
            <AgentConfigMcpServersField
              orgId={orgId}
              projectId={projectId}
              permissionProfile={toolCatalog.data?.mcp_tool_permissions}
              servers={form.mcpServers}
              onServersChange={form.setMcpServers}
            />
          </div>
        </div>
      </div>
    </FieldGroup>
  )
}
