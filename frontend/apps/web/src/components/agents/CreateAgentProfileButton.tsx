import { Link } from '@tanstack/react-router'

import { ChevronDown, Folder } from '@/components/icons'
import { NewProjectButton } from '@/components/projects/NewProjectButton'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useProjectDirectory } from '@/hooks/use-project-directory'

const label = 'Create agent profile'

/** Links to the new-profile form for a single project the viewer can manage. */
export function CreateAgentProfileButton({ projectId }: { projectId: string }) {
  return (
    <Button asChild size="sm">
      <Link to="/projects/$projectId/agents/new" params={{ projectId }}>
        {label}
      </Link>
    </Button>
  )
}

/**
 * Org-wide variant: profiles live in a project, so this links straight to the
 * only manageable project, asks which one when there are several, and offers
 * to create a project when there are none.
 */
export function OrgCreateAgentProfileButton({ orgId }: { orgId: string }) {
  const projects = [...useProjectDirectory(orgId).values()].filter(
    (project) => project.access.can_manage,
  )

  const [first, second] = projects
  if (first && !second) return <CreateAgentProfileButton projectId={first.id} />
  if (!first) return <NewProjectButton orgId={orgId} label="Create a project" />
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button size="sm">
          {label}
          <ChevronDown aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent className="max-h-72 min-w-56" align="center">
        <DropdownMenuLabel className="text-muted-foreground text-xs">
          Choose a project
        </DropdownMenuLabel>
        {projects.map((project) => (
          <DropdownMenuItem key={project.id} asChild className="gap-2">
            <Link to="/projects/$projectId/agents/new" params={{ projectId: project.id }}>
              <Folder />
              <span className="truncate">{project.name}</span>
            </Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
