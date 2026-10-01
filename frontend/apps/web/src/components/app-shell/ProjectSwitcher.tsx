import { useProjects } from '@omnara/react'
import { useNavigate, useParams, useRouterState } from '@tanstack/react-router'
import { useState } from 'react'

import { Check, ChevronsUpDown, Folder, LayoutGrid, Plus } from '@/components/icons'
import { NewProjectDialog } from '@/components/projects/NewProjectDialog'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useActiveOrg } from '@/lib/use-active-org'

import { currentSection, organizationPaths, projectPaths } from './scoped-sections'

export function ProjectSwitcher() {
  const { activeOrg } = useActiveOrg()
  const projectsQuery = useProjects(activeOrg.id)
  const projects = useInfiniteQueryItems(projectsQuery)
  const projectId = useParams({ strict: false, select: (params) => params.projectId })
  const section = useRouterState({ select: (state) => currentSection(state.location.pathname) })
  const navigate = useNavigate()
  const [newOpen, setNewOpen] = useState(false)
  const currentProject = projects.find((project) => project.id === projectId)
  const CurrentIcon = projectId ? Folder : LayoutGrid

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="sm" className="h-10 min-w-0 gap-2 px-2 font-medium md:h-8">
            <CurrentIcon className="text-muted-foreground size-4 shrink-0" />
            <span className="max-w-32 truncate sm:max-w-56">
              {projectId ? (currentProject?.name ?? 'Project') : 'All projects'}
            </span>
            <ChevronsUpDown className="text-muted-foreground size-4 shrink-0" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent className="min-w-60 rounded-lg" align="start" sideOffset={4}>
          <DropdownMenuItem
            className="gap-2"
            onClick={() => {
              void navigate({ to: section ? organizationPaths[section] : '/' })
            }}
          >
            <LayoutGrid />
            <span className="flex-1">All projects</span>
            {!projectId && <Check className="size-4 shrink-0" />}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuLabel className="text-muted-foreground text-xs">Projects</DropdownMenuLabel>
          {projects.length === 0 && (
            <p className="text-muted-foreground px-2 py-1.5 text-xs">No projects yet</p>
          )}
          {projects.map((project) => (
            <DropdownMenuItem
              key={project.id}
              className="gap-2"
              onClick={() => {
                void navigate({
                  to: section ? projectPaths[section] : '/projects/$projectId',
                  params: { projectId: project.id },
                })
              }}
            >
              <Folder />
              <span className="flex-1 truncate">{project.name}</span>
              {project.id === projectId && <Check className="size-4 shrink-0" />}
            </DropdownMenuItem>
          ))}
          {projectsQuery.hasNextPage && (
            <DropdownMenuItem
              disabled={projectsQuery.isFetchingNextPage}
              onSelect={(event) => {
                event.preventDefault()
                void projectsQuery.fetchNextPage()
              }}
            >
              {projectsQuery.isFetchingNextPage ? 'Loading…' : 'Load more projects'}
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onClick={() => {
              setNewOpen(true)
            }}
          >
            <Plus />
            New project
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <NewProjectDialog open={newOpen} onOpenChange={setNewOpen} orgId={activeOrg.id} />
    </>
  )
}
