import {
  type ProjectMachinePoolGrantListSort,
  useDeleteProjectMachinePoolGrant,
  useMachinePool,
  useProjectMachineGrants,
  useProjectMachinePoolGrants,
  useProjectMachines,
} from '@omnara/react'
import { ApiError, type ProjectMachinePoolGrantListItem } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useId, useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  agentCardLinkClass,
  AgentCardList,
  agentCardMoreLinkClass,
  AgentCardStatToggle,
} from '@/components/agents/AgentCardList'
import { ManagedLogo, OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { Monitor, Server } from '@/components/icons'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { machinePoolProviderLabel } from '@/components/org/MachinePoolDialogState'
import { MachinePoolProviderLogo } from '@/components/org/MachinePoolProviderLogo'
import { formatPoolMachines } from '@/components/overview/machinePoolFormat'
import { MachinePreviewList } from '@/components/overview/MachinePreviewList'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { EditMachinePoolGrantDialog } from '@/components/projects/EditMachinePoolGrantDialog'
import { poolGrantOverrides } from '@/components/projects/grant-override-diffs'
import { GrantMachineButton } from '@/components/projects/GrantMachineButton'
import { GrantMachinePoolButton } from '@/components/projects/GrantMachinePoolButton'
import { OverrideChip } from '@/components/projects/GrantOverrides'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { createdResourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatCount } from '@/lib/format'
import { canManageMachineGrants } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

const previewLimit = 5

/**
 * Machine pools and machines shared with the project. Anyone who can read the
 * project can view them; editing and revoking grants requires project access
 * management plus org management (the shared machines belong to the org).
 */
export function ProjectMachinesView({
  orgId,
  projectId,
  canManageAccess,
}: {
  orgId: string
  projectId: string
  canManageAccess: boolean
}) {
  const { activeOrg } = useActiveOrg()
  const canManageGrants = canManageMachineGrants(activeOrg.role, {
    can_manage_access: canManageAccess,
  })
  const list = useResourceList<ProjectMachinePoolGrantListSort>('-created_at')
  const grantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(grantsQuery, list.queryKey)
  const deleteGrant = useDeleteProjectMachinePoolGrant(orgId, projectId)
  const [editing, setEditing] = useState<ProjectMachinePoolGrantListItem | null>(null)

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Machines"
        description="Machine pools and machines agents in this project can run on"
        guide={guides.machinePools}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={{ value: list.sort, options: createdResourceSortOptions, onChange: list.setSort }}
            placeholder="Search shared pools by name…"
            showSearch
          />
        }
      >
        <GrantMachineButton />
        <GrantMachinePoolButton />
      </SearchHeader>
      {!list.isFiltering && <ProjectByoCard orgId={orgId} projectId={projectId} />}
      <AgentCardList
        items={paged.rows}
        getId={(item) => item.grant.id}
        renderCard={(item) => (
          <SharedPoolCard
            orgId={orgId}
            projectId={projectId}
            item={item}
            canManage={canManageGrants}
            onEdit={setEditing}
            onStopSharing={(grant) => {
              deleteGrant.mutate(grant.grant.id, {
                onError: (error) => {
                  window.alert(
                    error instanceof ApiError
                      ? error.message
                      : 'Could not stop sharing machine pool',
                  )
                },
              })
            }}
          />
        )}
        isFiltered={list.isFiltering}
        pagination={paged.pagination}
        isPending={grantsQuery.isPending}
        isError={grantsQuery.isError}
        onRetry={() => {
          void grantsQuery.refetch()
        }}
        emptyMessage={
          canManageGrants
            ? 'No shared machine pools. Share a pool so agents in this project can run.'
            : 'No machine pools are shared with this project yet.'
        }
        emptyAction={<GrantMachinePoolButton />}
      />
      {canManageGrants && editing && (
        <EditMachinePoolGrantDialog
          key={editing.grant.id}
          open
          onOpenChange={(open) => {
            if (!open) setEditing(null)
          }}
          orgId={orgId}
          projectId={projectId}
          item={editing}
        />
      )}
    </div>
  )
}

function SharedPoolActions({
  item,
  canManage,
  onEdit,
  onStopSharing,
}: {
  item: ProjectMachinePoolGrantListItem
  canManage: boolean
  onEdit: (item: ProjectMachinePoolGrantListItem) => void
  onStopSharing: (item: ProjectMachinePoolGrantListItem) => void
}) {
  if (!canManage) return null
  return (
    <ResourceRowActions
      deleteLabel="Stop sharing"
      onEdit={() => {
        onEdit(item)
      }}
      onDelete={() => {
        if (!window.confirm(`Stop sharing ${item.machine_pool.name} with this project?`)) return
        onStopSharing(item)
      }}
    />
  )
}

function SharedPoolCard({
  orgId,
  projectId,
  item,
  canManage,
  onEdit,
  onStopSharing,
}: {
  orgId: string
  projectId: string
  item: ProjectMachinePoolGrantListItem
  canManage: boolean
  onEdit: (item: ProjectMachinePoolGrantListItem) => void
  onStopSharing: (item: ProjectMachinePoolGrantListItem) => void
}) {
  const summary = item.machine_pool
  // The org pool supplies the defaults the project overrides; it may be hidden from the viewer.
  const { data: pool } = useMachinePool(orgId, summary.id)
  const overrides = poolGrantOverrides(item.grant, pool)
  const quota = item.grant.max_total_machines ?? pool?.max_total_machines
  const description = item.grant.description || summary.description
  const [expanded, setExpanded] = useState(false)
  const [prefetch, setPrefetch] = useState(false)
  const expansionId = useId()
  // Fetched only once opened (or hovered): one extra row tells us whether "View all" is needed.
  const preview = useProjectMachines(orgId, projectId, {
    filters: { machine_pool_id: summary.id },
    sort: '-updated_at',
    pageSize: previewLimit + 1,
    enabled: expanded || prefetch,
  })
  const firstPage = preview.data?.pages[0]?.data ?? []
  const expansion = {
    id: expansionId,
    open: expanded,
    content: (
      <MachinePreviewList
        machines={firstPage.slice(0, previewLimit)}
        isPending={preview.isPending}
        isError={preview.isError}
        emptyMessage="No machines yet. The pool provisions machines as this project's agents need them."
        viewAll={
          firstPage.length > previewLimit && (
            <Link
              to="/projects/$projectId/machines/pools/$poolId"
              params={{ projectId, poolId: summary.id }}
              className={agentCardMoreLinkClass}
            >
              View all machines →
            </Link>
          )
        }
      />
    ),
  }

  return (
    <AgentCard
      icon={
        <AgentCardGlyph>
          <ManagedLogo managed={summary.management_kind === 'cluster'}>
            <MachinePoolProviderLogo provider={summary.provider} />
          </ManagedLogo>
        </AgentCardGlyph>
      }
      title={
        <>
          <Link
            to="/projects/$projectId/machines/pools/$poolId"
            params={{ projectId, poolId: summary.id }}
            className={agentCardLinkClass}
          >
            {summary.name}
          </Link>
          {summary.management_kind === 'cluster' && <OmnaraManagedTag />}
          <OverrideChip count={overrides.length} />
        </>
      }
      subtitle={
        <>
          <span className="shrink-0">{machinePoolProviderLabel(summary.provider)}</span>
          {description && (
            <>
              <span aria-hidden="true">·</span>
              <span className="truncate">{description}</span>
            </>
          )}
        </>
      }
      meta={
        <SharedPoolActions
          item={item}
          canManage={canManage}
          onEdit={onEdit}
          onStopSharing={onStopSharing}
        />
      }
      footer={
        pool && (
          <span className="truncate tabular-nums">Org pool: {formatPoolMachines(pool)} in use</span>
        )
      }
      stats={
        <AgentCardStatToggle
          icon={Server}
          label="machines"
          value={quota === undefined ? undefined : `max ${formatCount(quota)}`}
          expansion={expansion}
          onToggle={() => {
            setExpanded((open) => !open)
          }}
          onPrefetch={() => {
            setPrefetch(true)
          }}
        />
      }
      expansion={expansion}
    />
  )
}

function ProjectByoCard({ orgId, projectId }: { orgId: string; projectId: string }) {
  // The machine-grants list holds only explicit (BYO) grants. One small page covers the
  // preview, and one extra row tells us whether there are more.
  const preview = useProjectMachineGrants(orgId, projectId, { pageSize: previewLimit + 1 })
  const firstPage = preview.data?.pages[0]?.data ?? []
  const hasMore = firstPage.length > previewLimit
  const machines = firstPage.slice(0, previewLimit).map((item) => item.machine)
  const [expanded, setExpanded] = useState(false)
  const expansionId = useId()
  const expansion = {
    id: expansionId,
    open: expanded,
    content: (
      <MachinePreviewList
        machines={machines}
        isPending={preview.isPending}
        isError={preview.isError}
        emptyMessage="No individual machines shared with this project."
        emptyAction={<GrantMachineButton />}
        viewAll={
          hasMore && (
            <Link
              to="/projects/$projectId/machines/byo"
              params={{ projectId }}
              className={agentCardMoreLinkClass}
            >
              View all machines →
            </Link>
          )
        }
      />
    ),
  }
  return (
    <AgentCard
      icon={
        <AgentCardGlyph>
          <Monitor aria-hidden="true" />
        </AgentCardGlyph>
      }
      title={
        <Link
          to="/projects/$projectId/machines/byo"
          params={{ projectId }}
          className={agentCardLinkClass}
        >
          BYO Machines
        </Link>
      }
      subtitle={<span className="truncate">Individual machines shared with this project</span>}
      meta={null}
      stats={
        <AgentCardStatToggle
          icon={Server}
          label={machines.length === 1 && !hasMore ? 'machine' : 'machines'}
          value={
            preview.isPending || preview.isError
              ? undefined
              : `${formatCount(machines.length)}${hasMore ? '+' : ''}`
          }
          expansion={expansion}
          onToggle={() => {
            setExpanded((open) => !open)
          }}
        />
      }
      expansion={expansion}
    />
  )
}
