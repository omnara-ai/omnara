import type { OtelExporterScope } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useId, useState } from 'react'

import { FetchedAgentReferenceLink } from '@/components/agents/AgentReferenceLinks'
import { AgentPicker } from '@/components/projects/EventWebhookScopeField'
import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useProjectDirectory } from '@/hooks/use-project-directory'

function scopeLabels(inProject: boolean) {
  return {
    org: 'All agents in the organization',
    project: inProject ? 'All agents in this project' : 'All agents in one project',
    agent: 'One agent',
  }
}

/**
 * Picks what an exporter covers. Inside a project the project is fixed, so the choice is
 * between the whole project and one of its agents.
 */
export function OtelExporterScopeField({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  /** The project the exporter is being added from, when there is one. */
  projectId?: string
  value: OtelExporterScope
  onChange: (value: OtelExporterScope) => void
}) {
  const id = useId()
  const labels = scopeLabels(projectId !== undefined)
  const [agent, setAgent] = useState<{ id: string; name: string }>()
  const selectedProjectId = value.type === 'org' ? '' : value.project_id
  return (
    <>
      <Field>
        <FieldLabel htmlFor={id}>Export traces from</FieldLabel>
        <Select
          value={value.type}
          onValueChange={(type) => {
            setAgent(undefined)
            const project_id = projectId ?? selectedProjectId
            if (type === 'org') onChange({ type })
            if (type === 'project') onChange({ type, project_id })
            if (type === 'agent') onChange({ type, project_id, agent_id: '' })
          }}
        >
          <SelectTrigger id={id}>
            <SelectValue>{labels[value.type]}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {projectId === undefined && <SelectItem value="org">{labels.org}</SelectItem>}
            <SelectItem value="project">{labels.project}</SelectItem>
            <SelectItem value="agent">{labels.agent}</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {projectId === undefined && value.type !== 'org' && (
        <ProjectPicker
          orgId={orgId}
          value={value.project_id}
          onChange={(project_id) => {
            setAgent(undefined)
            onChange(
              value.type === 'agent'
                ? { type: 'agent', project_id, agent_id: '' }
                : { type: 'project', project_id },
            )
          }}
        />
      )}
      {value.type === 'agent' && value.project_id && (
        <AgentPicker
          key={value.project_id}
          orgId={orgId}
          projectId={value.project_id}
          value={value.agent_id}
          selection={agent}
          onChange={(item) => {
            setAgent(item ?? undefined)
            onChange({ type: 'agent', project_id: value.project_id, agent_id: item?.id ?? '' })
          }}
        />
      )}
    </>
  )
}

function ProjectPicker({
  orgId,
  value,
  onChange,
}: {
  orgId: string
  value: string
  onChange: (projectId: string) => void
}) {
  const id = useId()
  const { loaded } = useProjectDirectory(orgId)
  const projects = [...loaded.values()].sort((left, right) => left.name.localeCompare(right.name))
  return (
    <Field>
      <FieldLabel htmlFor={id}>Project</FieldLabel>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger id={id}>
          <SelectValue placeholder="Choose a project…">{loaded.get(value)?.name}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          {projects.map((project) => (
            <SelectItem key={project.id} value={project.id}>
              {project.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

export function OtelExporterScopeLabel({
  orgId,
  projectId,
  scope,
}: {
  orgId: string
  /** The project the list is shown in, when there is one. */
  projectId?: string
  scope: OtelExporterScope
}) {
  const { loaded } = useProjectDirectory(orgId)
  if (scope.type === 'org') return <>All agents in the organization</>
  if (scope.type === 'agent')
    return (
      <span className="flex min-w-0 items-center gap-1">
        Agent:{' '}
        <FetchedAgentReferenceLink
          orgId={orgId}
          projectId={scope.project_id}
          agentId={scope.agent_id}
        />
      </span>
    )
  if (scope.project_id === projectId) return <>All agents in this project</>
  return (
    <span className="flex min-w-0 items-center gap-1">
      Project:{' '}
      <Link
        to="/projects/$projectId"
        params={{ projectId: scope.project_id }}
        className="truncate hover:underline"
      >
        {loaded.get(scope.project_id)?.name ?? scope.project_id}
      </Link>
    </span>
  )
}
