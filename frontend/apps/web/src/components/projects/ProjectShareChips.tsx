import { Combobox as ComboboxPrimitive } from '@base-ui/react/combobox'
import { useProjects } from '@omnara/react'
import type { VisibleProject } from '@omnara/sdk'
import { useState } from 'react'

import { Plus, X } from '@/components/icons'
import { Combobox, ComboboxInput } from '@/components/ui/combobox'
import { ResourceComboboxContent } from '@/components/ui/resource-combobox-content'
import type { ResourceComboboxConfig } from '@/components/ui/resource-combobox-core'
import { useProjectDirectory } from '@/hooks/use-project-directory'
import { cn } from '@/lib/utils'

const projectConfig: ResourceComboboxConfig<VisibleProject> = {
  itemKey: (project) => project.id,
  itemLabel: (project) => project.name,
  placeholder: 'Search projects…',
}

const noProjectIds: string[] = []

/** Compact "Share with" picker: chips for chosen projects and a searchable menu to add more. */
export function ProjectShareChips({
  orgId,
  value,
  onChange,
  disabled = false,
  excludedProjectIds = noProjectIds,
  failedProjectIds = noProjectIds,
  isProjectEligible,
}: {
  orgId: string
  value: string[]
  onChange: (projectIds: string[]) => void
  disabled?: boolean
  /** Projects never offered, e.g. the one that already owns the resource. */
  excludedProjectIds?: string[]
  /** Selected projects whose share failed, highlighted so they can be retried or removed. */
  failedProjectIds?: string[]
  isProjectEligible: (project: VisibleProject) => boolean
}) {
  const projectsQuery = useProjects(orgId)
  const { projects: directory, isLoaded } = useProjectDirectory(orgId)
  const [search, setSearch] = useState('')
  const excluded = new Set(excludedProjectIds)
  const failed = new Set(failedProjectIds)
  const projects = [...directory.values()].filter(
    (project) => isProjectEligible(project) && !excluded.has(project.id),
  )
  const selected = value.flatMap((projectId) => {
    const project = directory.get(projectId)
    return project ? [project] : []
  })

  function select(next: VisibleProject[]) {
    const nextIds = new Set(next.map((project) => project.id))
    // Keep selections the directory can't resolve; the picker never offered them.
    const kept = value.filter((projectId) => nextIds.has(projectId) || !directory.has(projectId))
    const keptIds = new Set(kept)
    onChange([...kept, ...next.flatMap((project) => (keptIds.has(project.id) ? [] : [project.id]))])
  }

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 text-sm">
      <span className="text-muted-foreground">Share with</span>
      {value.map((projectId) => {
        const name = directory.get(projectId)?.name ?? projectId
        const shareFailed = failed.has(projectId)
        return (
          <span
            key={projectId}
            title={shareFailed ? `Sharing with ${name} failed` : undefined}
            data-failed={shareFailed || undefined}
            className={cn(
              'bg-muted inline-flex h-8 max-w-48 items-center gap-1.5 rounded-lg pl-3 pr-2',
              shareFailed && 'bg-destructive/10 text-destructive ring-destructive/40 ring-1',
            )}
          >
            <span className="truncate">{name}</span>
            {shareFailed && <span className="sr-only">(sharing failed)</span>}
            <button
              type="button"
              aria-label={`Remove ${name}`}
              disabled={disabled}
              className={cn(
                'hover:text-foreground focus-visible:ring-ring/50 -m-1 rounded-sm p-1 outline-none focus-visible:ring-2 disabled:opacity-50',
                shareFailed ? 'text-destructive' : 'text-muted-foreground',
              )}
              onClick={() => {
                onChange(value.filter((id) => id !== projectId))
              }}
            >
              <X className="size-3.5" strokeWidth={2} />
            </button>
          </span>
        )
      })}
      <Combobox
        multiple
        items={projects}
        value={selected}
        onValueChange={select}
        inputValue={search}
        onInputValueChange={setSearch}
        onOpenChange={(open) => {
          if (!open) setSearch('')
        }}
        itemToStringLabel={projectConfig.itemLabel}
        itemToStringValue={projectConfig.itemKey}
        isItemEqualToValue={(project, other) => project.id === other.id}
        autoHighlight
        // Loading and load errors keep the menu reachable so it can say why it's empty.
        disabled={disabled || (isLoaded && projects.length === 0)}
      >
        <ComboboxPrimitive.Trigger
          aria-label="Add project"
          className="text-muted-foreground hover:text-foreground hover:border-muted-foreground focus-visible:ring-ring/50 inline-flex h-8 items-center gap-1.5 rounded-lg border border-dashed px-3 outline-none transition-colors focus-visible:ring-2 data-[disabled]:pointer-events-none data-[disabled]:opacity-50"
        >
          <Plus className="size-3.5" strokeWidth={2} />
          Project
        </ComboboxPrimitive.Trigger>
        <ResourceComboboxContent
          config={projectConfig}
          pending={!isLoaded && !projectsQuery.isError}
          emptyMessage="No matching projects."
          query={projectsQuery}
          searchInput={
            <ComboboxInput
              showTrigger={false}
              aria-label={projectConfig.placeholder}
              placeholder={projectConfig.placeholder}
            />
          }
        />
      </Combobox>
    </div>
  )
}
