import { useAgentProfiles, useCreateAgent, useCreateAgentConfig } from '@omnara/react'
import type { AgentProfile } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { type SyntheticEvent, useCallback, useEffect, useReducer, useState } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import {
  agentConfigModeReducer,
  initialAgentConfigModeState,
} from '@/components/agents/agentConfigModeMachine'
import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { AgentConfigYamlPreview } from '@/components/agents/AgentConfigYamlPreview'
import { AgentProfileTypeahead } from '@/components/agents/AgentProfileTypeahead'
import { ConfirmDiscardYamlDialog } from '@/components/agents/ConfirmDiscardYamlDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { FullPageSpinner, Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

type AgentCreateTab = 'profile' | 'builder' | 'yaml'

interface CreateAgentDraft {
  selectedProfile: AgentProfile | null
  name: string
  message: string
  status: SubmitStatus
}

export function CreateAgentPage() {
  const { activeOrg, project, isPending: projectIsPending } = useProjectPage()
  const params = useParams({ strict: false })
  const projectId = params.projectId ?? ''

  const createAgentConfig = useCreateAgentConfig(activeOrg.id, projectId)
  const createAgent = useCreateAgent(activeOrg.id, projectId)
  const navigate = useNavigate()
  const profileSearch = useTypeaheadSearch()
  const profilesQuery = useAgentProfiles(activeOrg.id, projectId, {
    filters: profileSearch.filters,
    sort: 'name',
    pageSize: 25,
  })
  const profiles = useInfiniteQueryItems(profilesQuery)
  const [mode, dispatchMode] = useReducer(
    agentConfigModeReducer<AgentCreateTab>,
    initialAgentConfigModeState<AgentCreateTab>('profile'),
  )
  const [draft, setDraft] = useState<CreateAgentDraft>({
    selectedProfile: null,
    name: '',
    message: '',
    status: idle,
  })
  // Stable identity: the builder's serialize effect depends on this callback.
  const handleBuilderYamlChange = useCallback((value: string) => {
    dispatchMode({ type: 'builder-yaml-changed', yaml: value })
  }, [])
  const defaultProfile =
    draft.selectedProfile === null && profileSearch.search === '' ? profiles[0] : undefined
  useEffect(() => {
    if (!defaultProfile) return
    setDraft((prev) =>
      prev.selectedProfile === null ? { ...prev, selectedProfile: defaultProfile } : prev,
    )
  }, [defaultProfile])

  if (projectIsPending) return <FullPageSpinner />

  if (!project?.access.can_operate) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="text-xl font-semibold tracking-tight">
          {project ? 'Not allowed' : 'Project not found'}
        </h1>
        <p className="text-muted-foreground text-sm">
          {project
            ? 'You don’t have permission to launch agents in this project.'
            : 'This project doesn’t exist or you don’t have access to it.'}
        </p>
      </div>
    )
  }

  const activeTab = mode.mode
  const isSubmitting = draft.status.phase === 'submitting'
  const errorMessage = statusError(draft.status)
  const selectedProfile = draft.selectedProfile
  const yaml = activeTab === 'builder' ? mode.builderYaml : mode.editorYaml
  const canSubmit =
    !isSubmitting &&
    (activeTab === 'profile'
      ? selectedProfile !== null && !profilesQuery.isPending
      : project.access.can_manage && yaml.trim() !== '')

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canSubmit) return
    setDraft((prev) => ({ ...prev, status: submitting }))
    try {
      const configId =
        activeTab === 'profile' && selectedProfile
          ? selectedProfile.current_config_id
          : (await createAgentConfig.mutateAsync({ source: yaml, source_format: 'yaml' })).id
      const launch = await createAgent.mutateAsync({
        profile: activeTab === 'profile' && selectedProfile ? selectedProfile.id : undefined,
        config: configId,
        message: draft.message.trim() === '' ? undefined : draft.message,
      })
      await navigate({
        to: '/projects/$projectId/agents/$agentId',
        params: { projectId, agentId: launch.agent.id },
      })
      setDraft((prev) => ({ ...prev, status: idle }))
    } catch (err) {
      setDraft((prev) => ({ ...prev, status: submitError(err, 'Could not create agent') }))
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
          { label: 'Agents', to: '/projects/$projectId/agents', params: { projectId } },
          { label: 'New agent' },
        ]}
      />
      <div className="flex flex-wrap items-center justify-end gap-3">
        {project.access.can_manage && (
          <PillTabs
            value={activeTab}
            onValueChange={(nextTab) => {
              dispatchMode({ type: 'switch-mode', mode: nextTab })
            }}
            tabs={[
              { value: 'profile', label: 'From profile' },
              { value: 'builder', label: 'Builder' },
              { value: 'yaml', label: 'YAML' },
            ]}
          />
        )}
        <Button type="submit" disabled={!canSubmit}>
          {isSubmitting && <Spinner />}
          Create agent
        </Button>
      </div>

      {errorMessage && (
        <p className="text-destructive whitespace-pre-wrap text-sm">{errorMessage}</p>
      )}

      <div
        className={cn(
          'grid gap-8',
          activeTab === 'builder' && 'lg:grid-cols-[minmax(0,1fr)_minmax(0,26rem)]',
        )}
      >
        <FieldGroup className={cn('gap-8', activeTab !== 'builder' && 'max-w-3xl')}>
          {activeTab === 'profile' && (
            <>
              <AgentProfileTypeahead
                profiles={profiles}
                selectedProfile={selectedProfile}
                search={profileSearch}
                query={profilesQuery}
                onSelect={(profile) => {
                  setDraft((prev) => ({ ...prev, selectedProfile: profile }))
                }}
              />
              {project.access.can_manage && (
                <FieldDescription>
                  Need a reusable config?{' '}
                  <Link
                    to="/projects/$projectId/agent-profiles/new"
                    params={{ projectId }}
                    className="text-foreground underline underline-offset-4"
                  >
                    Build an agent profile
                  </Link>
                  .
                </FieldDescription>
              )}
            </>
          )}
          {activeTab === 'builder' && (
            <Field>
              <FieldLabel htmlFor="agent-config-name">Agent name (optional)</FieldLabel>
              <Input
                id="agent-config-name"
                value={draft.name}
                placeholder="Demo research agent"
                className="max-w-md"
                onChange={(event) => {
                  setDraft((prev) => ({ ...prev, name: event.target.value }))
                }}
              />
            </Field>
          )}
          <div className={cn('flex flex-col gap-8', activeTab !== 'builder' && 'hidden')}>
            <AgentConfigBasicForm
              orgId={activeOrg.id}
              projectId={projectId}
              name={draft.name}
              onYamlChange={handleBuilderYamlChange}
            />
          </div>
          {activeTab === 'yaml' && (
            <AgentConfigYamlField
              id="agent-yaml"
              value={mode.editorYaml}
              showNamePlaceholder
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
              placeholder="Kick the agent off with a message"
              onChange={(event) => {
                setDraft((prev) => ({ ...prev, message: event.target.value }))
              }}
            />
          </Field>
        </FieldGroup>
        {activeTab === 'builder' && (
          <AgentConfigYamlPreview id="agent-yaml-preview" value={mode.builderYaml} />
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
