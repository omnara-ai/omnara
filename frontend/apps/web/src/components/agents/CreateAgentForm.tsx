import {
  useCreateAgent,
  useCreateAgentConfig,
  useCreateAgentProfile,
  useUpdateAgentProfile,
} from '@omnara/react'
import type { MachinePoolSummary, ToolCatalog } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { type SyntheticEvent, useCallback, useReducer, useState } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import {
  type BasicConfig,
  emptyBasicConfig,
} from '@/components/agents/agentConfigBasicSerialization'
import {
  agentConfigModeReducer,
  initialAgentConfigModeState,
} from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { AgentTemplateMenu } from '@/components/agents/AgentTemplateMenu'
import {
  type AgentTemplate,
  agentTemplateConfig,
  agentTemplateName,
} from '@/components/agents/agentTemplates'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

type SubmitAction = 'profile' | 'launch'

interface CreateAgentDraft {
  name: string
  message: string
  status: SubmitStatus
}

interface SavedProfile {
  name: string
  yaml: string
  profileId: string
  configId: string
}

export function CreateAgentForm({
  orgId,
  orgName,
  projectId,
  projectName,
  catalog,
  defaultPool,
  templatesReady,
  initialTemplate,
}: {
  orgId: string
  orgName: string
  projectId: string
  projectName: string
  catalog?: ToolCatalog
  defaultPool?: MachinePoolSummary
  templatesReady: boolean
  initialTemplate?: AgentTemplate
}) {
  const createAgentConfig = useCreateAgentConfig(orgId, projectId)
  const createAgentProfile = useCreateAgentProfile(orgId, projectId)
  const updateAgentProfile = useUpdateAgentProfile(orgId, projectId)
  const createAgent = useCreateAgent(orgId, projectId)
  const navigate = useNavigate()
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState('builder'),
  )
  const [draft, setDraft] = useState<CreateAgentDraft>(() => ({
    name: initialTemplate?.name ?? '',
    message: '',
    status: idle,
  }))
  const [configDraft, setConfigDraft] = useState<BasicConfig>(() =>
    initialTemplate && catalog
      ? { ...emptyBasicConfig, ...agentTemplateConfig(initialTemplate, catalog, defaultPool) }
      : emptyBasicConfig,
  )
  const [appliedTemplate, setAppliedTemplate] = useState<AgentTemplate | null>(
    initialTemplate ?? null,
  )
  const [pendingAction, setPendingAction] = useState<SubmitAction | null>(null)
  const [savedProfile, setSavedProfile] = useState<SavedProfile | null>(null)
  // Stable identity: the builder's serialize effect depends on this callback.
  const handleBuilderYamlChange = useCallback((value: string) => {
    dispatchMode({ type: 'builder-yaml-changed', yaml: value })
  }, [])

  function applyTemplate(template: AgentTemplate) {
    if (catalog == null) return
    setConfigDraft((prev) => ({ ...prev, ...agentTemplateConfig(template, catalog, defaultPool) }))
    setDraft((prev) => ({ ...prev, name: agentTemplateName(prev.name, template) }))
    setAppliedTemplate(template)
  }

  const showBuilder = mode.mode === 'builder'
  const isSubmitting = draft.status.phase === 'submitting'
  const errorMessage = statusError(draft.status)
  const yaml = showBuilder ? mode.builderYaml : mode.editorYaml
  const canSubmit = !isSubmitting && draft.name.trim() !== '' && yaml.trim() !== ''

  // Every agent belongs to a profile, so both actions save the config as a
  // profile first; "launch" additionally starts an agent from it.
  async function submit(action: SubmitAction) {
    if (!canSubmit) return
    setDraft((prev) => ({ ...prev, status: submitting }))
    setPendingAction(action)
    const name = draft.name.trim()
    try {
      let profile = savedProfile
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
        setSavedProfile(profile)
      }
      if (action === 'launch') {
        const launch = await createAgent.mutateAsync({
          profile: profile.profileId,
          config: profile.configId,
          message: draft.message.trim() === '' ? undefined : draft.message,
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
      setDraft((prev) => ({ ...prev, status: idle }))
    } catch (err) {
      setDraft((prev) => ({
        ...prev,
        status: submitError(
          err,
          action === 'launch' ? 'Could not create agent' : 'Could not create profile',
        ),
      }))
    } finally {
      setPendingAction(null)
    }
  }

  return (
    <form
      className="mx-auto flex w-full max-w-6xl flex-col gap-6"
      onSubmit={(event: SyntheticEvent<HTMLFormElement>) => {
        event.preventDefault()
        void submit('launch')
      }}
    >
      <PageBreadcrumb
        items={[
          { label: orgName, to: '/' },
          { label: projectName },
          { label: 'Agents', to: '/projects/$projectId/agents', params: { projectId } },
          { label: 'New agent' },
        ]}
      />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-bold tracking-tight">New agent</h1>
        <div className="flex items-center gap-2">
          {showBuilder && <AgentTemplateMenu disabled={!templatesReady} onApply={applyTemplate} />}
          <PillTabs
            value={mode.mode}
            onValueChange={(nextMode) => {
              dispatchMode({ type: 'switch-mode', mode: nextMode })
            }}
            tabs={[
              { value: 'builder', label: 'Builder' },
              { value: 'yaml', label: 'YAML' },
            ]}
          />
        </div>
      </div>

      <FieldGroup className="max-w-3xl gap-8">
        <Field>
          <FieldLabel htmlFor="agent-config-name">Name</FieldLabel>
          <Input
            id="agent-config-name"
            required
            value={draft.name}
            placeholder="Demo research agent"
            className="max-w-md"
            onChange={(event) => {
              setDraft((prev) => ({ ...prev, name: event.target.value }))
            }}
          />
        </Field>
        <div className={cn('flex flex-col gap-8', !showBuilder && 'hidden')}>
          <AgentConfigBasicForm
            orgId={orgId}
            projectId={projectId}
            draft={configDraft}
            onDraftChange={setConfigDraft}
            onYamlChange={handleBuilderYamlChange}
          />
        </div>
        {!showBuilder && (
          <AgentConfigYamlField
            id="agent-yaml"
            value={mode.editorYaml}
            className="h-[28rem]"
            onChange={(value) => {
              dispatchMode({ type: 'editor-yaml-changed', yaml: value })
            }}
          />
        )}
        <Field>
          <FieldLabel htmlFor="agent-message">First message (optional)</FieldLabel>
          <Input
            id="agent-message"
            value={draft.message}
            placeholder={
              appliedTemplate?.firstMessagePlaceholder ?? 'Kick the agent off with a message'
            }
            onChange={(event) => {
              setDraft((prev) => ({ ...prev, message: event.target.value }))
            }}
          />
        </Field>
      </FieldGroup>
      {/* -bottom-6 offsets the scroll container's p-6 so the bar sits flush with the viewport edge. */}
      <div className="bg-background sticky -bottom-6 z-10 -mb-6 flex items-center justify-between gap-4 border-t py-4">
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
          {errorMessage && (
            <p className="text-destructive whitespace-pre-wrap text-sm">{errorMessage}</p>
          )}
          <Button
            type="button"
            variant="outline"
            disabled={!canSubmit}
            onClick={() => {
              void submit('profile')
            }}
          >
            {pendingAction === 'profile' && <Spinner />}
            Create profile
          </Button>
          <Button type="submit" disabled={!canSubmit}>
            {pendingAction === 'launch' && <Spinner />}
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
