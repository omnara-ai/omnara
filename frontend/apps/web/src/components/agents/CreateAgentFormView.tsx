import { type ConfiguredModelSummary, type MachinePoolSummary, type ToolCatalog } from '@omnara/sdk'
import type { SyntheticEvent } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { AgentConfigIssueList } from '@/components/agents/AgentConfigIssueList'
import { AgentConfigModelField } from '@/components/agents/AgentConfigModelField'
import { yamlDiverged } from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { AgentTemplateMenu } from '@/components/agents/AgentTemplateMenu'
import type { AgentTemplate } from '@/components/agents/agentTemplates'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { CreateAgentActions } from '@/components/agents/CreateAgentActions'
import { PillTabs } from '@/components/agents/PillTabs'
import { useAgentDraft } from '@/components/agents/useAgentDraft'
import {
  type SubmitAction,
  useCreateAgentSubmission,
} from '@/components/agents/useCreateAgentSubmission'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Field, FieldGroup, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { resourceNameValid } from '@/lib/resource-name'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

export function CreateAgentFormView({
  catalog,
  defaultPool,
  defaultModel,
  templatesReady,
  initialTemplate,
}: {
  catalog?: ToolCatalog
  defaultPool?: MachinePoolSummary
  defaultModel?: ConfiguredModelSummary
  templatesReady: boolean
  initialTemplate?: AgentTemplate
}) {
  const { activeOrg, project, projectId } = useProjectPage()
  const {
    submit: submitAgent,
    status,
    pendingAction,
    launchError,
    issues,
  } = useCreateAgentSubmission(activeOrg.id, projectId)
  const { name, setName, mode, dispatchMode, form, switchMode, applyTemplate } = useAgentDraft(
    catalog,
    defaultPool,
    defaultModel,
    initialTemplate,
  )

  if (project == null) return null

  const showBuilder = mode.mode === 'builder'
  const isSubmitting = status.phase === 'submitting'
  const yaml = mode.editorYaml ?? form.yaml
  const canSubmit =
    !isSubmitting &&
    resourceNameValid(name) &&
    yaml.trim() !== '' &&
    !(form.blocked && (showBuilder || !yamlDiverged(mode)))

  async function submit(action: SubmitAction) {
    if (!canSubmit) return
    await submitAgent(name, yaml, action)
  }

  return (
    <form
      noValidate
      className="flex h-full w-full flex-col"
      onSubmit={(event: SyntheticEvent<HTMLFormElement>) => {
        event.preventDefault()
        void submit('launch')
      }}
    >
      {/* Negative margins offset the page padding so the scroll region and the pinned bar reach the pane edges. */}
      <div className="-mx-4 -mt-4 min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-4 sm:-mx-6 sm:-mt-6 sm:px-6 sm:pt-6">
        <div className="flex min-h-full w-full flex-col gap-6 pb-6">
          <PageBreadcrumb
            items={[
              { id: 'organization', label: activeOrg.name, to: '/' },
              { id: 'project', label: project.name },
              {
                id: 'agents',
                label: 'Agents',
                to: '/projects/$projectId/agents',
                params: { projectId },
              },
              { id: 'new-agent', label: 'New agent' },
            ]}
          />
          <div className="mx-auto flex w-full max-w-3xl flex-wrap items-center justify-between gap-2">
            <div>
              <h1 className="type-title">New agent</h1>
              <p className="text-muted-foreground mt-0.5 text-sm">
                Define a reusable agent profile for this project.
              </p>
            </div>
            <div className="flex items-center gap-2">
              {showBuilder && (
                <AgentTemplateMenu disabled={!templatesReady} onApply={applyTemplate} />
              )}
              <PillTabs
                value={mode.mode}
                onValueChange={switchMode}
                tabs={[
                  { value: 'builder', label: 'Builder' },
                  { value: 'yaml', label: 'YAML' },
                ]}
              />
            </div>
          </div>

          <FieldGroup className="mx-auto w-full max-w-3xl flex-1 gap-8">
            <div className={cn(showBuilder && 'grid gap-6 sm:grid-cols-2')}>
              <Field>
                <RequiredFieldLabel htmlFor="agent-config-name">Name</RequiredFieldLabel>
                <Input
                  id="agent-config-name"
                  required
                  value={name}
                  placeholder="Demo research agent"
                  className={cn(!showBuilder && 'max-w-md')}
                  onChange={(event) => {
                    setName(event.target.value)
                  }}
                />
                <ResourceNameFieldError value={name} />
              </Field>
              {showBuilder && (
                <AgentConfigModelField
                  orgId={activeOrg.id}
                  projectId={projectId}
                  value={form.model}
                  onChange={form.setModel}
                  onUnavailableChange={form.reportModelUnavailable}
                />
              )}
            </div>
            <div className={cn('flex flex-col gap-8', !showBuilder && 'hidden')}>
              <AgentConfigBasicForm
                orgId={activeOrg.id}
                projectId={projectId}
                form={form}
                agentName={name}
              />
              <AgentConfigIssueList issues={issues} />
            </div>
            {!showBuilder && (
              <AgentConfigYamlField
                id="agent-yaml"
                value={yaml}
                className="h-auto min-h-[24rem] flex-1"
                issues={issues}
                onChange={(value) => {
                  dispatchMode({ type: 'editor-yaml-changed', yaml: value, builderYaml: form.yaml })
                }}
              />
            )}
          </FieldGroup>
        </div>
      </div>
      <CreateAgentActions
        projectId={projectId}
        status={status}
        pendingAction={pendingAction}
        launchError={launchError}
        canSubmit={canSubmit}
        onCreateProfile={() => {
          void submit('profile')
        }}
      />
      <ConfirmDiscardYamlDialog
        open={mode.confirmDiscard}
        onOpenChange={(open) => {
          dispatchMode({ type: 'set-confirm-discard', open })
        }}
        onConfirm={() => {
          dispatchMode({ type: 'discard-yaml-edits' })
        }}
      />
    </form>
  )
}
