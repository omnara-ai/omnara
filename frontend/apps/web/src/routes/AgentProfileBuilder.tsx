import { useCreateAgentConfig, useCreateAgentProfile } from '@omnara/react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { type SyntheticEvent, useCallback, useReducer, useState } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import {
  agentConfigModeReducer,
  initialAgentConfigModeState,
} from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { AgentConfigYamlPreview } from '@/components/agents/AgentConfigYamlPreview'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { FullPageSpinner, Spinner } from '@/components/ui/spinner'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

type BuilderMode = 'builder' | 'yaml'

interface ProfileDraft {
  name: string
  status: SubmitStatus
}

export function AgentProfileBuilder() {
  const { activeOrg, project, isPending: projectIsPending } = useProjectPage()
  const params = useParams({ strict: false })
  const projectId = params.projectId ?? ''

  const createAgentConfig = useCreateAgentConfig(activeOrg.id, projectId)
  const createAgentProfile = useCreateAgentProfile(activeOrg.id, projectId)
  const navigate = useNavigate()
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer<BuilderMode>,
    initialAgentConfigModeState<BuilderMode>('builder'),
  )
  const [draft, setDraft] = useState<ProfileDraft>({ name: '', status: idle })
  // Stable identity: the builder's serialize effect depends on this callback.
  const handleBuilderYamlChange = useCallback((value: string) => {
    dispatchMode({ type: 'builder-yaml-changed', yaml: value })
  }, [])

  if (projectIsPending) return <FullPageSpinner />

  if (!project?.access.can_manage) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="text-xl font-semibold tracking-tight">
          {project ? 'Not allowed' : 'Project not found'}
        </h1>
        <p className="text-muted-foreground text-sm">
          {project
            ? 'You don’t have permission to manage agent profiles in this project.'
            : 'This project doesn’t exist or you don’t have access to it.'}
        </p>
      </div>
    )
  }

  const isSubmitting = draft.status.phase === 'submitting'
  const errorMessage = statusError(draft.status)
  const yaml = mode.mode === 'builder' ? mode.builderYaml : mode.editorYaml
  const canSubmit = !isSubmitting && draft.name.trim() !== '' && yaml.trim() !== ''

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canSubmit) return
    setDraft((prev) => ({ ...prev, status: submitting }))
    try {
      const config = await createAgentConfig.mutateAsync({
        source: yaml,
        source_format: 'yaml',
      })
      await createAgentProfile.mutateAsync({ name: draft.name.trim(), config: config.id })
      await navigate({ to: '/projects/$projectId/agents', params: { projectId } })
      setDraft((prev) => ({ ...prev, status: idle }))
    } catch (err) {
      setDraft((prev) => ({
        ...prev,
        status: submitError(err, 'Could not create agent profile'),
      }))
    }
  }

  return (
    <form
      className="mx-auto flex w-full max-w-6xl flex-col gap-6"
      onSubmit={(event) => {
        void submit(event)
      }}
    >
      <PageBreadcrumb
        items={[
          { label: activeOrg.name, to: '/' },
          { label: project.name },
          {
            label: 'Agents',
            to: '/projects/$projectId/agents',
            params: { projectId },
          },
          { label: 'New agent profile' },
        ]}
      />
      <div className="flex flex-wrap items-center justify-end gap-3">
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
        <Button type="submit" disabled={!canSubmit}>
          {isSubmitting && <Spinner />}
          Create profile
        </Button>
      </div>

      {errorMessage && (
        <p className="text-destructive whitespace-pre-wrap text-sm">{errorMessage}</p>
      )}

      <div
        className={cn(
          'grid gap-8',
          mode.mode === 'builder' && 'lg:grid-cols-[minmax(0,1fr)_minmax(0,26rem)]',
        )}
      >
        <div className="flex flex-col gap-8">
          <Field>
            <FieldLabel htmlFor="agent-profile-name">Profile name</FieldLabel>
            <Input
              id="agent-profile-name"
              required
              value={draft.name}
              placeholder="Demo research agent"
              className="max-w-md"
              onChange={(event) => {
                setDraft((prev) => ({ ...prev, name: event.target.value }))
              }}
            />
          </Field>
          <div className={cn('flex flex-col gap-8', mode.mode !== 'builder' && 'hidden')}>
            <AgentConfigBasicForm
              orgId={activeOrg.id}
              projectId={projectId}
              name={draft.name}
              onYamlChange={handleBuilderYamlChange}
            />
          </div>
          {mode.mode === 'yaml' && (
            <AgentConfigYamlField
              id="agent-profile-yaml"
              value={mode.editorYaml}
              showNamePlaceholder
              className="h-[36rem]"
              onChange={(value) => {
                dispatchMode({ type: 'editor-yaml-changed', yaml: value })
              }}
            />
          )}
        </div>
        {mode.mode === 'builder' && (
          <AgentConfigYamlPreview id="agent-profile-yaml-preview" value={mode.builderYaml} />
        )}
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
