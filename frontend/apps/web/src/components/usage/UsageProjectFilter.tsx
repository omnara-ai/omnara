import { useVisibleProjectsList } from '@omnara/react'

import { trailingCheckboxItemClass } from '@/components/data-table/FiltersMenu'
import {
  DropdownMenuCheckboxItem,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import type { UsageProjectFilterValue } from '@/components/usage/usage-project-filter'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

function keepMenuOpen(event: Event) {
  event.preventDefault()
}

export function UsageProjectMenuItems({
  orgId,
  value,
  onChange,
}: {
  orgId: string
  value: UsageProjectFilterValue
  onChange: (value: UsageProjectFilterValue) => void
}) {
  const projectsQuery = useVisibleProjectsList(orgId)
  const projects = useInfiniteQueryItems(projectsQuery)
  const selectedIds = new Set(value.map((project) => project.id))
  return (
    <>
      <DropdownMenuCheckboxItem
        className={trailingCheckboxItemClass}
        checked={value.length === 0}
        onSelect={keepMenuOpen}
        onCheckedChange={() => {
          onChange([])
        }}
      >
        All projects
      </DropdownMenuCheckboxItem>
      <DropdownMenuSeparator />
      {projects.map((project) => (
        <DropdownMenuCheckboxItem
          key={project.id}
          className={trailingCheckboxItemClass}
          checked={selectedIds.has(project.id)}
          onSelect={keepMenuOpen}
          onCheckedChange={(checked) => {
            onChange(
              checked
                ? [...value, project]
                : value.filter((selected) => selected.id !== project.id),
            )
          }}
        >
          <span className="truncate">{project.name}</span>
        </DropdownMenuCheckboxItem>
      ))}
      {projectsQuery.isPending && (
        <p className="text-muted-foreground px-2 py-1.5 text-sm" role="status">
          Loading projects…
        </p>
      )}
      {projectsQuery.isError && (
        <p className="text-destructive px-2 py-1.5 text-sm" role="alert">
          Could not load projects.
        </p>
      )}
      {(projectsQuery.hasNextPage || projectsQuery.isError) && (
        <DropdownMenuItem
          disabled={projectsQuery.isFetching}
          onSelect={(event) => {
            event.preventDefault()
            if (projectsQuery.isError && !projectsQuery.isFetchNextPageError) {
              void projectsQuery.refetch()
            } else {
              void projectsQuery.fetchNextPage()
            }
          }}
        >
          {projectsQuery.isFetching
            ? 'Loading…'
            : projectsQuery.isError
              ? 'Retry'
              : 'Load more projects'}
        </DropdownMenuItem>
      )}
      {projects.length === 0 && !projectsQuery.isPending && !projectsQuery.isError && (
        <p className="text-muted-foreground px-2 py-1.5 text-sm">No projects available.</p>
      )}
    </>
  )
}
