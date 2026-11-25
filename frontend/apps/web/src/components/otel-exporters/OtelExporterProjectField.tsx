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
 * Picks the project a new exporter belongs to, from the projects the viewer can manage.
 * Inside a project the project is fixed, so the field only states what is exported.
 */
export function OtelExporterProjectField({
  orgId,
  projectId,
  value,
  onChange,
}: {
  orgId: string
  /** The project the exporter is being added from, when there is one. */
  projectId?: string
  value: string
  onChange: (projectId: string) => void
}) {
  const id = useId()
  const { loaded } = useProjectDirectory(orgId)
  if (projectId !== undefined)
    return (
      <Field>
        <div className="type-label">Export traces from</div>
        <div className="flex min-h-9 items-center text-sm">All agents in this project</div>
      </Field>
    )
  const projects = [...loaded.values()]
    .filter((project) => project.access.can_manage)
    .sort((left, right) => left.name.localeCompare(right.name))
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

export function OtelExporterProjectLabel({
  orgId,
  currentProjectId,
  projectId,
}: {
  orgId: string
  /** The project the list is shown in, when there is one. */
  currentProjectId?: string
  /** The project the exporter belongs to. */
  projectId: string
}) {
  const { loaded } = useProjectDirectory(orgId)
  if (projectId === currentProjectId) return <>All agents in this project</>
  return (
    <span className="flex min-w-0 items-center gap-1">
      Project:{' '}
      <Link to="/projects/$projectId" params={{ projectId }} className="truncate hover:underline">
        {loaded.get(projectId)?.name ?? projectId}
      </Link>
    </span>
  )
}
