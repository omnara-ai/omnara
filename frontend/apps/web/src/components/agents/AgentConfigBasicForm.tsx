import { useToolCatalog } from '@omnara/react'

import { AgentConfigMachineSourcesField } from '@/components/agents/AgentConfigMachineSourcesField'
import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigMemoryField } from '@/components/agents/AgentConfigMemoryField'
import { AgentConfigSkillsField } from '@/components/agents/AgentConfigSkillsField'
import { AgentConfigSubagentsField } from '@/components/agents/AgentConfigSubagentsField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'
import { Field, FieldGroup, RequiredFieldLabel } from '@/components/ui/field'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'

export function AgentConfigBasicForm({
  orgId,
  projectId,
  form,
  agentName,
  onBeforeOAuthRedirect,
}: {
  orgId: string
  projectId: string
  form: AgentBuilderForm
  agentName?: string
  onBeforeOAuthRedirect?: () => void
}) {
  const toolCatalog = useToolCatalog()

  return (
    <FieldGroup className="gap-8">
      <Field>
        <RequiredFieldLabel htmlFor="agent-config-basic-instruction">
          Instructions
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
      <div className="flex flex-col gap-4">
        <div className="flex items-center gap-3 pt-2">
          <Separator className="flex-1" />
          <span className="text-muted-foreground shrink-0 text-xs">Optional</span>
        </div>
        <div className="flex flex-col gap-3">
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
            resolvedTools={form.resolvedTools}
            onToolsChange={form.setTools}
          />
          {form.toolsPending && (
            <p className="text-muted-foreground text-sm">Loading other tools…</p>
          )}
          {form.toolsError && (
            <p className="text-destructive text-sm" role="alert">
              Couldn’t load other tools.{' '}
              <button type="button" className="underline" onClick={form.retryTools}>
                Retry
              </button>
            </p>
          )}
          <AgentConfigSkillsField
            orgId={orgId}
            projectId={projectId}
            selectedIds={form.skillIds}
            onSelectedIdsChange={form.setSkillIds}
            onUnavailableIdsChange={form.reportUnavailableSkillIds}
          />
          <AgentConfigSubagentsField
            orgId={orgId}
            projectId={projectId}
            subagents={form.subagents}
            maxSubagents={form.maxSubagents}
            maxDepth={form.maxDepth}
            onSubagentsChange={form.setSubagents}
            onMaxSubagentsChange={form.setMaxSubagents}
            onMaxDepthChange={form.setMaxDepth}
          />
          <AgentConfigMcpServersField
            orgId={orgId}
            projectId={projectId}
            permissionProfile={toolCatalog.data?.mcp_tool_permissions}
            servers={form.mcpServers}
            onServersChange={form.setMcpServers}
            builderDraft={form.draft}
            agentName={agentName}
            onBeforeOAuthRedirect={onBeforeOAuthRedirect}
          />
          <AgentConfigMemoryField
            orgId={orgId}
            projectId={projectId}
            stores={form.memoryStores}
            onChange={form.setMemoryStores}
          />
        </div>
      </div>
    </FieldGroup>
  )
}
