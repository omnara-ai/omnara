import { useProjects } from '@omnara/react'
import { Link, useRouterState } from '@tanstack/react-router'
import { ChevronDown, Folder, Plus } from 'lucide-react'
import { useState } from 'react'

import { NewProjectDialog } from '@/components/projects/NewProjectDialog'
import {
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
} from '@/components/ui/sidebar'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useActiveOrg } from '@/lib/use-active-org'

export function ProjectsNav() {
  const { activeOrg } = useActiveOrg()
  const projectsQuery = useProjects(activeOrg.id)
  const projects = useInfiniteQueryItems(projectsQuery)
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const [newOpen, setNewOpen] = useState(false)
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(() => new Set())

  return (
    <SidebarGroup>
      <SidebarGroupLabel>Projects</SidebarGroupLabel>
      <SidebarGroupAction
        title="New project"
        onClick={() => {
          setNewOpen(true)
        }}
      >
        <Plus />
        <span className="sr-only">New project</span>
      </SidebarGroupAction>
      <SidebarGroupContent>
        <SidebarMenu>
          {projects.length === 0 ? (
            <p className="text-muted-foreground px-2 py-1.5 text-xs">No projects yet</p>
          ) : (
            projects.map((project) => {
              const projectRoot = `/projects/${project.id}`
              const expanded = !collapsedProjects.has(project.id)
              const resources = [
                {
                  to: '/projects/$projectId/agents' as const,
                  path: `${projectRoot}/agent`,
                  label: 'Agents',
                  prefix: true,
                },
                {
                  to: '/projects/$projectId/grants' as const,
                  path: `${projectRoot}/grants`,
                  label: 'Grants',
                },
                {
                  to: '/projects/$projectId/secrets' as const,
                  path: `${projectRoot}/secrets`,
                  label: 'Project Secrets',
                },
                {
                  to: '/projects/$projectId/skills' as const,
                  path: `${projectRoot}/skills`,
                  label: 'Project Skills',
                },
              ]

              return (
                <SidebarMenuItem key={project.id}>
                  <SidebarMenuButton
                    tooltip={project.name}
                    aria-expanded={expanded}
                    aria-controls={`project-${project.id}-resources`}
                    onClick={() => {
                      setCollapsedProjects((current) => {
                        const next = new Set(current)
                        if (next.has(project.id)) next.delete(project.id)
                        else next.add(project.id)
                        return next
                      })
                    }}
                  >
                    <Folder />
                    <span>{project.name}</span>
                    <ChevronDown
                      className={`ml-auto transition-transform duration-200 motion-reduce:transition-none ${expanded ? '' : '-rotate-90'}`}
                    />
                  </SidebarMenuButton>
                  <div
                    className={`grid transition-[grid-template-rows,opacity] duration-200 ease-out motion-reduce:transition-none ${expanded ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0'}`}
                  >
                    <div className="min-h-0 overflow-hidden" inert={!expanded}>
                      <SidebarMenuSub id={`project-${project.id}-resources`}>
                        {resources.map((resource) => (
                          <SidebarMenuSubItem key={resource.to}>
                            <SidebarMenuSubButton
                              asChild
                              isActive={
                                resource.prefix
                                  ? pathname.startsWith(resource.path)
                                  : pathname === resource.path
                              }
                            >
                              <Link to={resource.to} params={{ projectId: project.id }}>
                                <span>{resource.label}</span>
                              </Link>
                            </SidebarMenuSubButton>
                          </SidebarMenuSubItem>
                        ))}
                      </SidebarMenuSub>
                    </div>
                  </div>
                </SidebarMenuItem>
              )
            })
          )}
          {projectsQuery.hasNextPage && (
            <SidebarMenuItem>
              <SidebarMenuButton
                disabled={projectsQuery.isFetchingNextPage}
                onClick={() => {
                  void projectsQuery.fetchNextPage()
                }}
              >
                <span>{projectsQuery.isFetchingNextPage ? 'Loading…' : 'Load more projects'}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          )}
        </SidebarMenu>
      </SidebarGroupContent>
      <NewProjectDialog open={newOpen} onOpenChange={setNewOpen} orgId={activeOrg.id} />
    </SidebarGroup>
  )
}
