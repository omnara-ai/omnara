import { useCreateAgentConfig, useCreateAgentProfile } from '@omnara/react'
import type { VisibleProject } from '@omnara/sdk'
import { useState } from 'react'

import { AgentConfigBasicForm } from '@/components/agents/AgentConfigBasicForm'
import { AgentConfigModelField } from '@/components/agents/AgentConfigModelField'
import { agentTemplates } from '@/components/agents/agentTemplates'
import {
  type BasicConfig,
  createBasicConfigSession,
  useAgentBuilderForm,
} from '@/components/agents/useAgentBuilderForm'
import { useTemplateDefaults } from '@/components/overview/useTemplateDefaults'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

export interface ProfileDraft {
  name: string
  config: BasicConfig
}

export function ProfileDraftStep({
  orgId,
  project,
  disabled = false,
}: {
  orgId: string
  project: VisibleProject
  disabled?: boolean
}) {
  const [templateId, setTemplateId] = useState(agentTemplates[0]?.id ?? '')
  const [customDraft, setCustomDraft] = useState<ProfileDraft | null>(null)
  const [builderOpen, setBuilderOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const defaults = useTemplateDefaults(orgId, project.id)
  const createAgentConfig = useCreateAgentConfig(orgId, project.id)
  const createAgentProfile = useCreateAgentProfile(orgId, project.id)

  const template = agentTemplates.find((candidate) => candidate.id === templateId)
  const draft = (): ProfileDraft | null => {
    if (customDraft) return customDraft
    if (!template) return null
    return { name: template.name, config: defaults.build(template) }
  }
  const canCreate =
    defaults.ready && !submitting && !disabled && template != null && defaults.defaultModel != null

  async function create() {
    const current = draft()
    if (!current) return
    setSubmitting(true)
    setError('')
    try {
      const yaml = createBasicConfigSession('').apply(current.config)
      const config = await createAgentConfig.mutateAsync({ source: yaml, source_format: 'yaml' })
      await createAgentProfile.mutateAsync({ name: current.name, config: config.id })
    } catch (err) {
      setError(errorMessage(err, 'Could not create profile'))
    }
    setSubmitting(false)
  }

  if (builderOpen) {
    const current = draft()
    if (current) {
      return (
        <InlineProfileBuilder
          orgId={orgId}
          projectId={project.id}
          initial={current}
          onDone={(next) => {
            setCustomDraft(next)
            setBuilderOpen(false)
          }}
        />
      )
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <div role="radiogroup" aria-label="Agent template" className="grid gap-4 sm:grid-cols-3">
        {agentTemplates.map((candidate) => {
          const selected = candidate.id === templateId
          return (
            <button
              key={candidate.id}
              type="button"
              role="radio"
              aria-checked={selected}
              data-template={candidate.id}
              disabled={submitting}
              className={cn(
                'flex min-h-36 flex-col gap-2 rounded-xl border p-6 text-left transition-all',
                'focus-visible:ring-ring/50 focus-visible:outline-none focus-visible:ring-[3px]',
                selected
                  ? 'border-blue-500/40 bg-gradient-to-br from-blue-500/[0.06] via-blue-500/[0.02] to-transparent'
                  : 'border-border bg-card hover:bg-muted/50',
              )}
              onClick={() => {
                setTemplateId(candidate.id)
                setCustomDraft(null)
              }}
            >
              <span className="text-sm font-semibold">{candidate.name}</span>
              <span className="text-muted-foreground text-xs leading-snug">
                {candidate.description}
              </span>
            </button>
          )
        })}
      </div>
      {customDraft && (
        <p className="text-muted-foreground text-sm" data-slot="customized-note">
          Customized as <span className="text-foreground font-medium">{customDraft.name}</span>.
        </p>
      )}
      {defaults.ready && defaults.defaultModel == null && (
        <p className="text-destructive text-sm">
          Grant a model to {project.name} before creating a profile.
        </p>
      )}
      {error && <p className="text-destructive text-sm">{error}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          data-action="create-profile"
          disabled={!canCreate}
          loading={submitting}
          onClick={() => {
            void create()
          }}
        >
          Create profile
        </Button>
        <Button
          type="button"
          variant="ghost"
          className="text-primary hover:text-primary"
          data-action="customize"
          disabled={!canCreate}
          onClick={() => {
            setBuilderOpen(true)
          }}
        >
          Customize first
        </Button>
      </div>
    </div>
  )
}

function InlineProfileBuilder({
  orgId,
  projectId,
  initial,
  onDone,
}: {
  orgId: string
  projectId: string
  initial: ProfileDraft
  onDone: (draft: ProfileDraft) => void
}) {
  const [name, setName] = useState(initial.name)
  const [session] = useState(() => createBasicConfigSession(''))
  const form = useAgentBuilderForm(session, initial.config)
  const valid = resourceNameValid(name) && !form.blocked

  return (
    <div className="flex flex-col gap-6" data-slot="inline-builder">
      <FieldGroup className="gap-6">
        <div className="grid gap-6 sm:grid-cols-2">
          <Field>
            <RequiredFieldLabel htmlFor="onboarding-profile-name">Name</RequiredFieldLabel>
            <Input
              id="onboarding-profile-name"
              required
              value={name}
              onChange={(event) => {
                setName(event.target.value)
              }}
            />
            <ResourceNameFieldError value={name} />
          </Field>
          <AgentConfigModelField
            orgId={orgId}
            projectId={projectId}
            value={form.model}
            onChange={form.setModel}
            onUnavailableChange={form.reportModelUnavailable}
          />
        </div>
        <AgentConfigBasicForm orgId={orgId} projectId={projectId} form={form} agentName={name} />
      </FieldGroup>
      <div className="flex justify-end">
        <Button
          type="button"
          data-action="builder-ok"
          disabled={!valid}
          onClick={() => {
            onDone({ name, config: form.draft })
          }}
        >
          OK
        </Button>
      </div>
    </div>
  )
}
