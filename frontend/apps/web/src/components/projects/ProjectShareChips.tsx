import type { VisibleProject } from '@omnara/sdk'

import { Plus, X } from '@/components/icons'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useProjectDirectory } from '@/hooks/use-project-directory'

/** Compact "Share with" picker: chips for chosen projects and a menu to add more. */
export function ProjectShareChips({
  orgId,
  value,
  onChange,
  disabled = false,
  excludedProjectIds = [],
  isProjectEligible,
}: {
  orgId: string
  value: string[]
  onChange: (projectIds: string[]) => void
  disabled?: boolean
  /** Projects never offered, e.g. the one that already owns the resource. */
  excludedProjectIds?: string[]
  isProjectEligible: (project: VisibleProject) => boolean
}) {
  const { projects: directory } = useProjectDirectory(orgId)
  const available = [...directory.values()].filter(
    (project) =>
      isProjectEligible(project) &&
      !value.includes(project.id) &&
      !excludedProjectIds.includes(project.id),
  )
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 text-sm">
      <span className="text-muted-foreground">Share with</span>
      {value.map((projectId) => {
        const name = directory.get(projectId)?.name ?? projectId
        return (
          <span
            key={projectId}
            className="bg-muted inline-flex h-8 max-w-48 items-center gap-1.5 rounded-lg pl-3 pr-2"
          >
            <span className="truncate">{name}</span>
            <button
              type="button"
              aria-label={`Remove ${name}`}
              disabled={disabled}
              className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 -m-1 rounded-sm p-1 outline-none focus-visible:ring-2 disabled:opacity-50"
              onClick={() => {
                onChange(value.filter((id) => id !== projectId))
              }}
            >
              <X className="size-3.5" strokeWidth={2} />
            </button>
          </span>
        )
      })}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className="text-muted-foreground hover:text-foreground hover:border-muted-foreground focus-visible:ring-ring/50 inline-flex h-8 items-center gap-1.5 rounded-lg border border-dashed px-3 outline-none transition-colors focus-visible:ring-2 disabled:pointer-events-none disabled:opacity-50"
            disabled={disabled || available.length === 0}
          >
            <Plus className="size-3.5" strokeWidth={2} />
            Project
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="max-h-72 overflow-y-auto">
          {available.map((project) => (
            <DropdownMenuItem
              key={project.id}
              onSelect={() => {
                onChange([...value, project.id])
              }}
            >
              {project.name}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
