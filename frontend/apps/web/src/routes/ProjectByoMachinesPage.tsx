import {
  type ProjectMachineGrantListSort,
  useDeleteProjectMachineGrant,
  useProjectMachineGrants,
} from '@omnara/react'
import { ApiError } from '@omnara/sdk'
import { useParams } from '@tanstack/react-router'

import { AgentCard, AgentCardGlyph } from '@/components/agents/AgentCardList'
import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { Monitor } from '@/components/icons'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { machineStatusLabel } from '@/components/overview/machineFormat'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { GrantMachineButton } from '@/components/projects/GrantMachineButton'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatDateTime, formatTimeAgo } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function ProjectByoMachinesPage() {
  const { projectId = '' } = useParams({ strict: false })
  return (
    <ProjectPageFrame
      crumbs={[
        {
          id: 'machines',
          label: 'Machines',
          to: '/projects/$projectId/machines',
          params: { projectId },
        },
        { id: 'byo', label: 'BYO Machines' },
      ]}
    >
      {({ activeOrg, project }) =>
        project?.access.can_read ? (
          <ProjectByoMachines
            orgId={activeOrg.id}
            projectId={projectId}
            canManageAccess={project.access.can_manage_access}
          />
        ) : (
          <p className="text-muted-foreground text-sm">
            You don&rsquo;t have permission to view shared machines in this project.
          </p>
        )
      }
    </ProjectPageFrame>
  )
}

function ProjectByoMachines({
  orgId,
  projectId,
  canManageAccess,
}: {
  orgId: string
  projectId: string
  canManageAccess: boolean
}) {
  const { activeOrg } = useActiveOrg()
  const canStopSharing = canManageAccess && canManageOrg(activeOrg.role)
  const list = useResourceList<ProjectMachineGrantListSort>('-updated_at')
  // The machine-grants list holds only explicit (BYO) grants; pool machines live on their pool's page.
  const grantsQuery = useProjectMachineGrants(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(grantsQuery, list.queryKey)
  const deleteGrant = useDeleteProjectMachineGrant(orgId, projectId)

  return (
    <>
      <AgentCard
        icon={
          <AgentCardGlyph>
            <Monitor aria-hidden="true" />
          </AgentCardGlyph>
        }
        title={<h1 className="truncate font-medium">BYO Machines</h1>}
        subtitle={<span className="truncate">Individual machines shared with this project</span>}
        meta={null}
      />
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Machines"
          description="Machines you connect and run yourself, shared one at a time"
          guide={guides.machines}
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
              placeholder="Search machines by name…"
              showSearch
            />
          }
        >
          <GrantMachineButton />
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (item) => (
                <span className="truncate font-medium">{item.machine.display_name}</span>
              ),
            },
            {
              id: 'status',
              header: 'Status',
              className: 'w-36',
              cell: (item) => (
                <span className="capitalize">{machineStatusLabel(item.machine)}</span>
              ),
            },
            {
              id: 'last-seen',
              header: 'Last seen',
              className: 'w-36',
              cell: (item) => {
                const lastSeen = item.machine.last_observed_at ?? item.machine.updated_at
                return (
                  <span
                    className="text-muted-foreground tabular-nums"
                    title={formatDateTime(lastSeen)}
                  >
                    {formatTimeAgo(lastSeen)}
                  </span>
                )
              },
            },
            {
              id: 'actions',
              header: '',
              className: 'w-14',
              isActions: true,
              cell: (item) => (
                <ResourceRowActions
                  deleteLabel="Stop sharing"
                  onDelete={
                    canStopSharing
                      ? () => {
                          if (
                            !window.confirm(
                              `Stop sharing ${item.machine.display_name} with this project?`,
                            )
                          )
                            return
                          deleteGrant.mutate(item.grant.id, {
                            onError: (error) => {
                              window.alert(
                                error instanceof ApiError
                                  ? error.message
                                  : 'Could not stop sharing machine',
                              )
                            },
                          })
                        }
                      : undefined
                  }
                />
              ),
            },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(item) => item.grant.id}
          rowExpanded={(item) => (
            <DetailList
              items={[
                { label: 'Machine ID', value: item.machine.id, mono: true },
                { label: 'Description', value: item.grant.description || item.machine.description },
                { label: 'Connection', value: item.machine.connection_state },
                { label: 'Shared', value: formatDateTime(item.grant.created_at) },
              ]}
            />
          )}
          isPending={grantsQuery.isPending}
          isError={grantsQuery.isError}
          onRetry={() => {
            void grantsQuery.refetch()
          }}
          emptyMessage="No individual machines shared with this project."
          emptyAction={<GrantMachineButton />}
        />
      </div>
    </>
  )
}
