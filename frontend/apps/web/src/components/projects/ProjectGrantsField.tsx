import { useProjects } from '@omnara/react'
import type { VisibleProject } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceMultiCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

const ProjectMultiCombobox = createResourceMultiCombobox<VisibleProject>({
  itemKey: (project) => project.id,
  itemLabel: (project) => project.name,
  placeholder: 'Search projects…',
})

export function ProjectGrantsField({
  orgId,
  value,
  onChange,
  disabled = false,
  description = 'Selected projects receive access as soon as this resource is created.',
  excludedProjectIds = [],
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
  const projects = useInfiniteQueryItems(projectsQuery).filter(
    (project) => isProjectEligible(project) && !excludedProjectIds.includes(project.id),
  )

  return (
    <Field>
      <FieldLabel>Project grants</FieldLabel>
      <ProjectMultiCombobox
        items={projects}
        value={value}
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
