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

/** Project-scoped creation pages an org-wide button can lead to. */
export type ProjectCreatePath =
  | '/projects/$projectId/agents/new'
  | '/projects/$projectId/integrations/new'

/**
 * Starts creating something that lives in a project from an org-wide page: links straight to
 * the only manageable project, asks which one when there are several, and can offer to create
 * a project when there are none.
 */
export function ProjectPickerButton({
  orgId,
  to,
  label,
  offerNewProject = false,
}: {
  orgId: string
  to: ProjectCreatePath
  label: string
  /**
   * Offer to create a project when the viewer can't manage any; otherwise render nothing.
   * Only org admins can create projects, so callers pass canManageOrg.
   */
  offerNewProject?: boolean
}) {
  const directory = useProjectDirectory(orgId)
  const projects = [...directory.projects.values()].filter((project) => project.access.can_manage)
  // Choosing between one project, several, or none needs the whole list.
  if (!directory.isLoaded) return null

  const [first, second] = projects
  if (first && !second)
    return (
      <Button asChild size="sm">
        <Link to={to} params={{ projectId: first.id }}>
          {label}
        </Link>
      </Button>
    )
  if (!first)
    return offerNewProject ? <NewProjectButton orgId={orgId} label="Create a project" /> : null
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
            <Link to={to} params={{ projectId: project.id }}>
              <Folder />
              <span className="truncate">{project.name}</span>
            </Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
