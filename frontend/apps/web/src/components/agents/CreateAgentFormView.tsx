import {
  useCreateAgent,
  useCreateAgentConfig,
  useCreateAgentProfile,
  useUpdateAgentProfile,
} from '@omnara/react'
import type { AgentConfigErrorIssue, ApiError } from '@omnara/sdk'
import { type ConfiguredModelSummary, type MachinePoolSummary, type ToolCatalog } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { type SyntheticEvent, useReducer, useRef, useState } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { AgentConfigIssueList } from '@/components/agents/AgentConfigIssueList'
import { AgentConfigModelField } from '@/components/agents/AgentConfigModelField'
import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
  yamlDiverged,
} from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { AgentTemplateMenu } from '@/components/agents/AgentTemplateMenu'
import {
  type AgentTemplate,
  agentTemplateBasicConfig,
  agentTemplateName,
  defaultAgentTools,
} from '@/components/agents/agentTemplates'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import { takeMcpBuilderOAuthRestore } from '@/components/agents/pendingMcpBuilderOAuth'
import { PillTabs } from '@/components/agents/PillTabs'
import {
  createBasicConfigSession,
  emptyBasicConfig,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { configSubmitError } from '@/lib/agent-config-issues'
import { isInsufficientCreditsError } from '@/lib/insufficient-credits'
import { resourceNameValid } from '@/lib/resource-name'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, settleSubmission, statusError, submitting } from '@/lib/submit-status'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'
import { useWebConfig } from '@/lib/web-config'

type SubmitAction = 'profile' | 'launch'

interface CreateAgentDraft {
  name: string
  status: SubmitStatus
}

interface SavedProfile {
  name: string
  yaml: string
  profileId: string
  configId: string
}

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
  const createAgentConfig = useCreateAgentConfig(activeOrg.id, projectId)
  const createAgentProfile = useCreateAgentProfile(activeOrg.id, projectId)
  const updateAgentProfile = useUpdateAgentProfile(activeOrg.id, projectId)
  const createAgent = useCreateAgent(activeOrg.id, projectId)
  const { data: webConfig } = useWebConfig()
  const navigate = useNavigate()
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState('builder'),
  )
  const [restored] = useState(takeMcpBuilderOAuthRestore)
  const [draft, setDraft] = useState<CreateAgentDraft>(() => ({
    name: restored?.agentName ?? initialTemplate?.name ?? '',
    status: idle,
  }))
  const [pendingAction, setPendingAction] = useState<SubmitAction | null>(null)
  const [launchError, setLaunchError] = useState<ApiError>()
  const savedProfile = useRef<SavedProfile | null>(null)
  const [session, setSession] = useState(() => createBasicConfigSession(''))
  const form = useAgentBuilderForm(
    session,
    restored?.draft ??
      (initialTemplate
        ? agentTemplateBasicConfig(initialTemplate, catalog, defaultPool, defaultModel)
        : { ...emptyBasicConfig, tools: defaultAgentTools(catalog) }),
  )
  const switchMode = (nextMode: AgentConfigMode) => {
    if (nextMode === 'builder' && mode.editorYaml !== null) {
      const adopted = createBasicConfigSession(mode.editorYaml)
      if (adopted.initialDraft != null) {
        setSession(adopted)
        form.reset(adopted.initialDraft)
        dispatchMode({ type: 'adopt-yaml-edits' })
        return
      }
    }
    dispatchMode({ type: 'switch-mode', mode: nextMode })
  }

  function applyTemplate(template: AgentTemplate) {
    const next = agentTemplateBasicConfig(template, catalog, defaultPool, defaultModel)
    // Keep a model the user already picked; templates only fill the gap.
    if (form.model.providerConfig !== '' && form.model.modelName !== '') {
      next.providerConfig = form.model.providerConfig
      next.modelName = form.model.modelName
    }
    form.reset(next)
    setDraft((prev) => ({ ...prev, name: agentTemplateName(prev.name, template) }))
  }

  const [issues, setIssues] = useState<AgentConfigErrorIssue[]>([])

  if (project == null) return null

  const showBuilder = mode.mode === 'builder'
  const isSubmitting = draft.status.phase === 'submitting'
  const errorMessage = statusError(draft.status)
  const yaml = mode.editorYaml ?? form.yaml
  const canSubmit =
    !isSubmitting &&
    resourceNameValid(draft.name) &&
    yaml.trim() !== '' &&
    !(form.blocked && (showBuilder || !yamlDiverged(mode)))

  async function submit(action: SubmitAction) {
    if (!canSubmit) return
    setLaunchError(undefined)
    setIssues([])
    setDraft((prev) => ({ ...prev, status: submitting }))
    setPendingAction(action)
    const name = draft.name
    const result = await settleSubmission(async () => {
      let profile = savedProfile.current
      if (profile?.name !== name || profile.yaml !== yaml) {
        const config = await createAgentConfig.mutateAsync({ source: yaml, source_format: 'yaml' })
        if (profile?.name === name) {
          await updateAgentProfile.mutateAsync({
            agentProfileID: profile.profileId,
            config: config.id,
            expected_current_config_id: profile.configId,
          })
          profile = { ...profile, yaml, configId: config.id }
        } else {
          const created = await createAgentProfile.mutateAsync({ name, config: config.id })
          profile = { name, yaml, profileId: created.id, configId: config.id }
        }
        savedProfile.current = profile
      }
      if (action === 'launch') {
        const launch = await createAgent.mutateAsync({
          profile: profile.profileId,
          config: profile.configId,
        })
        await navigate({
          to: '/projects/$projectId/agents/$agentId',
          params: { projectId, agentId: launch.agent.id },
        })
      } else {
        await navigate({
          to: '/projects/$projectId/agent-profiles/$profileId',
          params: { projectId, profileId: profile.profileId },
        })
      }
    }).finally(() => {
      setPendingAction(null)
    })

    if (result.ok) {
      setDraft((prev) => ({ ...prev, status: idle }))
    } else {
      if (action === 'launch' && isInsufficientCreditsError(result.error)) {
        setLaunchError(result.error)
      }
      const failure = configSubmitError(
        result.error,
        action === 'launch' ? 'Could not create agent' : 'Could not create profile',
      )
      setIssues(failure.issues)
      setDraft((prev) => ({
        ...prev,
        status: { phase: 'error', message: failure.message },
      }))
    }
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
      {/* Negative margins offset the scroll container's p-6 so the scroll region and the pinned bar reach the pane edges. */}
      <div className="-mx-6 -mt-6 min-h-0 flex-1 overflow-y-auto overscroll-contain px-6 pt-6">
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
              <h1 className="text-2xl font-bold tracking-tight">New agent</h1>
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
                  value={draft.name}
                  placeholder="Demo research agent"
                  className={cn(!showBuilder && 'max-w-md')}
                  onChange={(event) => {
                    setDraft((prev) => ({ ...prev, name: event.target.value }))
                  }}
                />
                <ResourceNameFieldError value={draft.name} />
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
                agentName={draft.name}
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
      <div className="bg-sidebar -mx-6 -mb-6 flex items-center justify-between gap-4 border-t px-8 py-3.5">
        <Button
          type="button"
          variant="ghost"
          disabled={isSubmitting}
          onClick={() => {
            void navigate({ to: '/projects/$projectId/agents', params: { projectId } })
          }}
        >
          Cancel
        </Button>
        <div className="flex items-center gap-4">
          {launchError && webConfig?.billingURL ? (
            <p className="text-destructive whitespace-pre-wrap text-sm" role="alert">
              <InsufficientCreditsMessage billingHref={webConfig.billingHref} />
            </p>
          ) : errorMessage ? (
            <p className="text-destructive whitespace-pre-wrap text-sm">{errorMessage}</p>
          ) : null}
          <Button
            type="button"
            variant="outline"
            disabled={!canSubmit}
            loading={pendingAction === 'profile'}
            onClick={() => {
              void submit('profile')
            }}
          >
            Create profile
          </Button>
          <Button type="submit" disabled={!canSubmit} loading={pendingAction === 'launch'}>
            Create & launch agent
          </Button>
        </div>
      </div>
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
