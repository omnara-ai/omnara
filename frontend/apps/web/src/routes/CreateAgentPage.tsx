<<<<<<< HEAD
import { CreateAgentForm } from '@/components/agents/CreateAgentForm'
import { FullPageSpinner } from '@/components/ui/spinner'
import { useProjectPage } from '@/lib/use-project-page'
=======
import {
  useCreateAgent,
  useCreateAgentConfig,
  useCreateAgentProfile,
  useUpdateAgentProfile,
} from '@omnara/react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { type SyntheticEvent, useReducer, useRef, useState } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import {
  type AgentConfigMode,
  agentConfigModeReducer,
  initialAgentConfigModeState,
  yamlDiverged,
} from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import {
  createBasicConfigSession,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { FullPageSpinner, Spinner } from '@/components/ui/spinner'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'
import { useProjectPage } from '@/lib/use-project-page'
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
>>>>>>> cc153ffda (review fixes, adapt to document based yaml)

export function CreateAgentPage() {
  const { project, isPending } = useProjectPage()

<<<<<<< HEAD
  if (isPending) return <FullPageSpinner />
=======
  const createAgentConfig = useCreateAgentConfig(activeOrg.id, projectId)
  const createAgentProfile = useCreateAgentProfile(activeOrg.id, projectId)
  const updateAgentProfile = useUpdateAgentProfile(activeOrg.id, projectId)
  const createAgent = useCreateAgent(activeOrg.id, projectId)
  const navigate = useNavigate()
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer,
    initialAgentConfigModeState('builder'),
  )
  const [draft, setDraft] = useState<CreateAgentDraft>({
    name: '',
    message: '',
    status: idle,
  })
  const [pendingAction, setPendingAction] = useState<SubmitAction | null>(null)
  const savedProfile = useRef<SavedProfile | null>(null)
  const [session, setSession] = useState(() => createBasicConfigSession(''))
  const form = useAgentBuilderForm(session)
  const [builderYaml, setBuilderYaml] = useState('')
  if (!form.blocked && builderYaml !== form.yaml) setBuilderYaml(form.yaml)
  const switchMode = (nextMode: AgentConfigMode) => {
    if (nextMode === 'builder' && mode.editorYaml !== null) {
      const adopted = createBasicConfigSession(mode.editorYaml)
      if (adopted.initialDraft != null) {
        setSession(adopted)
        form.reset(adopted.initialDraft)
        setBuilderYaml(mode.editorYaml)
        dispatchMode({ type: 'adopt-yaml-edits' })
        return
      }
    }
    dispatchMode({ type: 'switch-mode', mode: nextMode })
  }

  if (projectIsPending) return <FullPageSpinner />
>>>>>>> cc153ffda (review fixes, adapt to document based yaml)

  if (!project?.access.can_manage) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="text-xl font-semibold tracking-tight">
          {project ? 'Not allowed' : 'Project not found'}
        </h1>
        <p className="text-muted-foreground text-sm">
          {project
            ? 'You don’t have permission to create agents in this project.'
            : 'This project doesn’t exist or you don’t have access to it.'}
        </p>
      </div>
    )
  }

<<<<<<< HEAD
  return <CreateAgentForm />
=======
  const showBuilder = mode.mode === 'builder'
  const isSubmitting = draft.status.phase === 'submitting'
  const errorMessage = statusError(draft.status)
  const editorYaml = mode.editorYaml ?? builderYaml
  const yaml = showBuilder ? builderYaml : editorYaml
  const canSubmit =
    !isSubmitting &&
    draft.name.trim() !== '' &&
    yaml.trim() !== '' &&
    !(form.blocked && (showBuilder || !yamlDiverged(mode)))

  async function submit(action: SubmitAction) {
    if (!canSubmit) return
    setDraft((prev) => ({ ...prev, status: submitting }))
    setPendingAction(action)
    const name = draft.name.trim()
    try {
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
          { label: activeOrg.name, to: '/' },
          { label: project.name },
          { label: 'Agents', to: '/projects/$projectId/agents', params: { projectId } },
          { label: 'New agent' },
        ]}
      />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-bold tracking-tight">New agent</h1>
        <PillTabs
          value={mode.mode}
          onValueChange={switchMode}
          tabs={[
            { value: 'builder', label: 'Builder' },
            { value: 'yaml', label: 'YAML' },
          ]}
        />
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
          <AgentConfigBasicForm orgId={activeOrg.id} projectId={projectId} form={form} />
        </div>
        {!showBuilder && (
          <AgentConfigYamlField
            id="agent-yaml"
            value={editorYaml}
            className="h-[28rem]"
            onChange={(value) => {
              dispatchMode({ type: 'editor-yaml-changed', yaml: value, builderYaml })
            }}
          />
        )}
        <Field>
          <FieldLabel htmlFor="agent-message">First message (optional)</FieldLabel>
          <Input
            id="agent-message"
            value={draft.message}
            placeholder="Kick the agent off with a message"
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
>>>>>>> cc153ffda (review fixes, adapt to document based yaml)
}
