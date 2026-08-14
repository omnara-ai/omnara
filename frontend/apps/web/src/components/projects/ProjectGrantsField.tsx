import { useProjects } from '@omnara/react'
import type { VisibleProject } from '@omnara/sdk'
import { useState } from 'react'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

const ProjectMultiCombobox = createResourceMultiCombobox<VisibleProject>({
  itemKey: (project) => project.id,
  itemLabel: (project) => project.name,
  placeholder: 'Search projects…',
})

const noExcludedProjectIds: string[] = []

function useSelectedProjects(projects: VisibleProject[], selectedProjectIds: string[]) {
  const [retainedProjects, setRetainedProjects] = useState<VisibleProject[]>([])
  const availableProjects = new Map(projects.map((project) => [project.id, project]))
  const retainedProjectMap = new Map(retainedProjects.map((project) => [project.id, project]))
  const selectedProjects = selectedProjectIds.flatMap((projectId) => {
    const project = availableProjects.get(projectId) ?? retainedProjectMap.get(projectId)
    return project ? [project] : []
  })

  if (
    selectedProjects.length !== retainedProjects.length ||
    selectedProjects.some((project, index) => project !== retainedProjects[index])
  ) {
    setRetainedProjects(selectedProjects)
  }

  return selectedProjects
}

export function ProjectGrantsField({
  orgId,
  value,
  onChange,
  disabled = false,
  description = 'Selected projects receive access as soon as this resource is created.',
  excludedProjectIds = noExcludedProjectIds,
  isProjectEligible,
}: {
  orgId: string
  value: string[]
  onChange: (projectIds: string[]) => void
  disabled?: boolean
  description?: string
  excludedProjectIds?: string[]
  isProjectEligible: (project: VisibleProject) => boolean
}) {
  const projectsQuery = useProjects(orgId)
  const excludedProjectIdSet = new Set(excludedProjectIds)
  const projects = useInfiniteQueryItems(projectsQuery).filter(
    (project) => isProjectEligible(project) && !excludedProjectIdSet.has(project.id),
  )
  const selectedProjects = useSelectedProjects(projects, value)

  return (
    <Field>
      <FieldLabel>Project grants</FieldLabel>
      <ProjectMultiCombobox
        items={projects}
        value={selectedProjects}
        onValueChange={(nextProjects) => {
          onChange(nextProjects.map((project) => project.id))
        }}
        query={projectsQuery}
        emptyMessage={
          projects.length > 0 ? 'All matching projects selected.' : 'No projects available.'
        }
        disabled={disabled}
      />
      <FieldDescription>
        {projectsQuery.isError ? 'Could not load grantable projects. Try again.' : description}
      </FieldDescription>
    </Field>
  )
}
