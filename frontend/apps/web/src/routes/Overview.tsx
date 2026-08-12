import { useMe, useProjects } from '@omnara/react'
import type { VisibleProject } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'

import { DataTable } from '@/components/data-table/DataTable'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { readRecentPages } from '@/lib/recent-pages'
import { useActiveOrg } from '@/lib/use-active-org'

interface RecentPageRow {
  path: string
  visitedAt: string
  title: string
  context: string
}

const visitedAtFormatter = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: 'numeric',
  minute: '2-digit',
})

export function Overview() {
  const { activeOrg } = useActiveOrg()
  const { data: me } = useMe()
  const navigate = useNavigate()
  const projectsQuery = useProjects(activeOrg.id)
  const projects = useInfiniteQueryItems(projectsQuery)

  const projectById = new Map(projects.map((project) => [project.id, project]))
  const recentPages: RecentPageRow[] = readRecentPages(activeOrg.id, me.user.id)
    .flatMap((page) => {
      const description = describePage(page.path, projectById)
      if (!description) return []
      return [
        {
          path: page.path,
          visitedAt: page.visitedAt,
          title: description.title,
          context: description.context,
        },
      ]
    })
    .slice(0, 5)

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ label: activeOrg.name }, { label: 'Overview' }]} />

      <div className="flex flex-col gap-3">
        <SearchHeader title="Recent pages" />
        <DataTable
          columns={[
            {
              id: 'page',
              header: 'Page',
              cell: (page) => <span className="font-medium">{page.title}</span>,
            },
            { id: 'location', header: 'Location', cell: (page) => page.context },
            {
              id: 'visited',
              header: 'Visited',
              className: 'w-44',
              cell: (page) => (
                <span className="text-muted-foreground">{formatVisitedAt(page.visitedAt)}</span>
              ),
            },
          ]}
          data={recentPages}
          getRowId={(page) => page.path}
          onRowClick={(page) => {
            void navigate({ href: page.path })
          }}
          emptyMessage="Pages you visit in this organization will appear here."
        />
      </div>
    </div>
  )
}

function describePage(path: string, projects: Map<string, VisibleProject>) {
  const organizationPages: Record<string, { title: string; context: string }> = {
    '/members': { title: 'Members', context: 'Organization' },
    '/machines': { title: 'Machines', context: 'Organization' },
    '/models': { title: 'Models', context: 'Organization' },
    '/secrets': { title: 'Secrets', context: 'Organization' },
    '/skills': { title: 'Skills', context: 'Organization' },
    '/user/api-tokens': { title: 'API Tokens', context: 'Organization' },
    '/device': { title: 'Device authorization', context: 'Account' },
  }
  if (organizationPages[path]) return organizationPages[path]

  const match = /^\/projects\/([^/]+)(?:\/(.*))?$/.exec(path)
  if (!match) return { title: 'Page', context: 'Organization' }
  const projectId = match[1]
  if (!projectId) return null
  const project = projects.get(projectId)
  if (!project) return null
  const suffix = match[2] ?? ''
  if (/^agent-profiles\/[^/]+$/.test(suffix))
    return { title: 'Agent profile', context: project.name }
  if (suffix === 'agents/new') return { title: 'New agent', context: project.name }
  if (/^agents\/[^/]+$/.test(suffix)) return { title: 'Agent', context: project.name }
  if (suffix === 'agents') return { title: 'Agents', context: project.name }
  if (suffix === 'grants') return { title: 'Grants', context: project.name }
  if (suffix === 'secrets') return { title: 'Project Secrets', context: project.name }
  if (suffix === 'skills') return { title: 'Project Skills', context: project.name }
  return { title: project.name, context: 'Project' }
}

function formatVisitedAt(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Recently visited'
  return visitedAtFormatter.format(date)
}
