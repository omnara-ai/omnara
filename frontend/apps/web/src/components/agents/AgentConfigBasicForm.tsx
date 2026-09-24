import { useToolCatalog } from '@omnara/react'

import { AgentConfigEventWebhookField } from '@/components/agents/AgentConfigEventWebhookField'
import { AgentConfigMachineSourcesField } from '@/components/agents/AgentConfigMachineSourcesField'
import { AgentConfigMcpServersField } from '@/components/agents/AgentConfigMcpServersField'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { AgentConfigSkillsField } from '@/components/agents/AgentConfigSkillsField'
import { AgentConfigSubagentsField } from '@/components/agents/AgentConfigSubagentsField'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'
import { ChevronRightIcon } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
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
    <FieldGroup className="gap-5">
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
          {Object.keys(form.interactionHandlers).length > 0 && (
            <AgentConfigSectionCard title="Integration capabilities" action={null}>
              <div className="space-y-2 px-4 pb-4 text-sm sm:px-5">
                <p>Interaction handlers: {Object.keys(form.interactionHandlers).join(', ')}</p>
                <p className="text-muted-foreground">
                  Edit interaction settings in YAML. Sending tools are configured separately.
                </p>
              </div>
            </AgentConfigSectionCard>
          )}
          {form.toolsPending && (
            <p className="text-muted-foreground text-sm">Loading built-in tools…</p>
          )}
          {form.toolsError && (
            <p className="text-destructive text-sm" role="alert">
              {form.toolsErrorMessage}{' '}
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
          <Collapsible className="pt-2">
            <CollapsibleTrigger className="text-muted-foreground hover:text-foreground focus-visible:ring-ring group flex items-center gap-1.5 rounded-sm py-1 text-left text-sm focus-visible:ring-2 focus-visible:ring-offset-2">
              <ChevronRightIcon className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
              Advanced
            </CollapsibleTrigger>
            <CollapsibleContent className="pt-4">
              <FieldGroup className="bg-card rounded-xl border px-4 py-4 sm:px-5">
                <AgentConfigEventWebhookField
                  orgId={orgId}
                  projectId={projectId}
                  events={form.eventWebhookEvents}
                  onEventsChange={form.setEventWebhookEvents}
                  url={form.eventWebhookUrl}
                  signingSecretId={form.eventWebhookSigningSecretId}
                  onUrlChange={form.setEventWebhookUrl}
                  onSigningSecretIdChange={form.setEventWebhookSigningSecretId}
                />
              </FieldGroup>
            </CollapsibleContent>
          </Collapsible>
        </div>
      </div>
    </FieldGroup>
  )
}
