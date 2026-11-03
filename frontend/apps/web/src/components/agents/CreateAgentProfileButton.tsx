import { Link } from '@tanstack/react-router'

import { ProjectPickerButton } from '@/components/projects/ProjectPickerButton'
import { Button } from '@/components/ui/button'

const defaultLabel = 'Create agent profile'

/** Links to the new-profile form for a single project the viewer can manage. */
export function CreateAgentProfileButton({
  projectId,
  label = defaultLabel,
}: {
  projectId: string
  label?: string
}) {
  return (
    <Button asChild size="sm">
      <Link to="/projects/$projectId/agents/new" params={{ projectId }}>
        {label}
      </Link>
    </Button>
  )
}

/** Org-wide variant: profiles live in a project, so this picks one first. */
export function OrgCreateAgentProfileButton({
  orgId,
  label = defaultLabel,
  offerNewProject = false,
}: {
  orgId: string
  label?: string
  /** Offer to create a project when the viewer can't manage any. */
  offerNewProject?: boolean
}) {
  return (
    <ProjectPickerButton
      orgId={orgId}
      to="/projects/$projectId/agents/new"
      label={label}
      offerNewProject={offerNewProject}
    />
  )
}
