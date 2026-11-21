import type { OtelExporterScope } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useId } from 'react'

import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useProjectDirectory } from '@/hooks/use-project-directory'

/**
 * Picks the project an exporter covers. Inside a project the project is fixed, so there is
 * nothing to choose and the field only states what is exported.
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
  if (projectId !== undefined)
    return (
      <Field>
        <div className="type-label">Export traces from</div>
        <div className="flex min-h-9 items-center text-sm">All agents in this project</div>
      </Field>
    )
  return (
    <ProjectPicker
      orgId={orgId}
      value={value.project_id}
      onChange={(project_id) => {
        onChange({ type: 'project', project_id })
      }}
    />
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
      <FieldLabel htmlFor={id}>Export traces from</FieldLabel>
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
