import {
  type ProjectMachinePoolGrantListSort,
  useCreateProjectMachinePoolGrant,
  useDeleteProjectMachinePoolGrant,
  useMachinePools,
  useProjectMachineGrants,
  useProjectMachinePoolGrants,
  useProjectMachines,
} from '@omnara/react'
import { ApiError, type MachinePool, type ProjectMachinePoolGrantListItem } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useId, useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  agentCardLinkClass,
  agentCardMoreLinkClass,
  AgentCardStatToggle,
} from '@/components/agents/AgentCardList'
import { ManagedLogo, OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { Monitor, Plus, Server } from '@/components/icons'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { machinePoolProviderLabel } from '@/components/org/MachinePoolDialogState'
import { MachinePoolProviderLogo } from '@/components/org/MachinePoolProviderLogo'
import { formatPoolMachines } from '@/components/overview/machinePoolFormat'
import { MachinePreviewList } from '@/components/overview/MachinePreviewList'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { EditMachinePoolGrantDialog } from '@/components/projects/EditMachinePoolGrantDialog'
import { poolGrantOverrides } from '@/components/projects/grant-override-diffs'
import { GrantMachinePoolButton } from '@/components/projects/GrantMachinePoolButton'
import {
  emptyPoolGrantDraft,
  poolGrantCreateRequest,
} from '@/components/projects/GrantMachinePoolDialogState'
import { OverrideChip } from '@/components/projects/GrantOverrides'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useAllPages } from '@/hooks/use-all-pages'
import { createdResourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatCount } from '@/lib/format'
import { clusterFirst } from '@/lib/management-kind'
import { canManageMachineGrants } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

const previewLimit = 5

/**
 * Machine pools and machines shared with the project. Anyone who can read the
 * project can view them; sharing, editing and revoking grants requires project
 * access management plus org management (the shared machines belong to the org).
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
  const grants = useAllPages(
    useProjectMachinePoolGrants(orgId, projectId, { filters: list.apiFilters, sort: list.sort }),
  )
  const pools = useAllPages(useMachinePools(orgId))
  const createGrant = useCreateProjectMachinePoolGrant(orgId, projectId)
  const deleteGrant = useDeleteProjectMachinePoolGrant(orgId, projectId)
  const [showAvailable, setShowAvailable] = useState(false)
  const [editing, setEditing] = useState<ProjectMachinePoolGrantListItem | null>(null)
  const [sharingId, setSharingId] = useState<string | null>(null)
  const poolById = new Map(pools.items.map((pool) => [pool.id, pool]))
  const shared = [...grants.items].sort(
    (left, right) =>
      Number(right.machine_pool.management_kind === 'cluster') -
      Number(left.machine_pool.management_kind === 'cluster'),
  )
  const sharedPoolIds = new Set(grants.items.map((item) => item.grant.machine_pool_id))
  const available =
    canManageGrants && showAvailable && !list.isFiltering
      ? clusterFirst(pools.items.filter((pool) => !sharedPoolIds.has(pool.id)))
      : []

  async function share(pool: MachinePool) {
    setSharingId(pool.id)
    try {
      await createGrant.mutateAsync(poolGrantCreateRequest(pool, emptyPoolGrantDraft()))
    } catch (error) {
      window.alert(error instanceof ApiError ? error.message : 'Could not share machine pool')
    }
    setSharingId(null)
  }

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
        {canManageGrants && (
          <Button
            size="sm"
            variant="ghost"
            aria-pressed={showAvailable}
            onClick={() => {
              setShowAvailable((value) => !value)
            }}
          >
            {showAvailable ? 'Hide available' : 'Show available'}
          </Button>
        )}
        <GrantMachinePoolButton />
      </SearchHeader>
      {!list.isFiltering && <ProjectByoCard orgId={orgId} projectId={projectId} />}
      {grants.isPending ? (
        <Skeleton className="h-[7.25rem] rounded-xl" />
      ) : grants.isError ? (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>Couldn&rsquo;t load shared machine pools.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : shared.length === 0 && available.length === 0 ? (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>
              {list.isFiltering
                ? 'No results.'
                : canManageGrants
                  ? 'No shared machine pools. Share a pool so agents in this project can run.'
                  : 'No machine pools are shared with this project yet.'}
            </EmptyDescription>
          </EmptyHeader>
          {!list.isFiltering && (
            <EmptyContent className="flex-row flex-wrap justify-center gap-2">
              <GrantMachinePoolButton />
            </EmptyContent>
          )}
        </Empty>
      ) : (
        <ul className="flex flex-col gap-5">
          {shared.map((item) => (
            <li key={item.grant.id}>
              <SharedPoolCard
                orgId={orgId}
                projectId={projectId}
                item={item}
                pool={poolById.get(item.grant.machine_pool_id)}
                actions={
                  <ResourceRowActions
                    deleteLabel="Stop sharing"
                    onEdit={
                      canManageGrants
                        ? () => {
                            setEditing(item)
                          }
                        : undefined
                    }
                    onDelete={
                      canManageGrants
                        ? () => {
                            if (
                              !window.confirm(
                                `Stop sharing ${item.machine_pool.name} with this project?`,
                              )
                            )
                              return
                            deleteGrant.mutate(item.grant.id)
                          }
                        : undefined
                    }
                  />
                }
              />
            </li>
          ))}
          {available.map((pool) => (
            <li key={pool.id}>
              <AvailablePoolCard
                pool={pool}
                sharing={sharingId === pool.id}
                onShare={() => {
                  void share(pool)
                }}
              />
            </li>
          ))}
        </ul>
      )}
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

function SharedPoolCard({
  orgId,
  projectId,
  item,
  pool,
  actions,
}: {
  orgId: string
  projectId: string
  item: ProjectMachinePoolGrantListItem
  pool: MachinePool | undefined
  actions: ReactNode
}) {
  const summary = item.machine_pool
  const machines = useAllPages(
    useProjectMachines(orgId, projectId, { filters: { machine_pool_id: summary.id } }),
  )
  const overrides = poolGrantOverrides(item.grant, pool)
  const quota = item.grant.max_total_machines ?? pool?.max_total_machines
  const inUse = machines.isPending || machines.isError ? undefined : machines.items.length
  const description = item.grant.description || summary.description
  const [expanded, setExpanded] = useState(false)
  const expansionId = useId()
  const expansion = {
    id: expansionId,
    open: expanded,
    content: (
      <MachinePreviewList
        machines={machines.items.slice(0, previewLimit)}
        isPending={machines.isPending}
        isError={machines.isError}
        emptyMessage="No machines yet. The pool provisions machines as this project's agents need them."
        viewAll={
          machines.items.length > previewLimit && (
            <Link
              to="/projects/$projectId/machines/pools/$poolId"
              params={{ projectId, poolId: summary.id }}
              className={agentCardMoreLinkClass}
            >
              View all {formatCount(machines.items.length)} machines →
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
      meta={actions}
      footer={
        pool && (
          <span className="truncate tabular-nums">Org pool: {formatPoolMachines(pool)} in use</span>
        )
      }
      stats={
        <AgentCardStatToggle
          icon={Server}
          label="in this project"
          value={
            inUse === undefined
              ? undefined
              : quota === undefined
                ? formatCount(inUse)
                : `${formatCount(inUse)} / ${formatCount(quota)}`
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

function AvailablePoolCard({
  pool,
  sharing,
  onShare,
}: {
  pool: MachinePool
  sharing: boolean
  onShare: () => void
}) {
  return (
    <div className="opacity-70 transition-opacity hover:opacity-100">
      <AgentCard
        icon={
          <AgentCardGlyph>
            <ManagedLogo managed={pool.management_kind === 'cluster'}>
              <MachinePoolProviderLogo provider={pool.provider} />
            </ManagedLogo>
          </AgentCardGlyph>
        }
        title={
          <>
            <span className="truncate font-medium">{pool.name}</span>
            {pool.management_kind === 'cluster' && <OmnaraManagedTag />}
          </>
        }
        subtitle={
          <>
            <span className="shrink-0">{machinePoolProviderLabel(pool.provider)}</span>
            <span aria-hidden="true">·</span>
            <span className="truncate">Not shared with this project</span>
          </>
        }
        meta={
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="text-primary hover:text-primary h-9 px-2 sm:h-7"
            loading={sharing}
            onClick={onShare}
          >
            <Plus aria-hidden="true" />
            Share
          </Button>
        }
      />
    </div>
  )
}

function ProjectByoCard({ orgId, projectId }: { orgId: string; projectId: string }) {
  const grants = useAllPages(useProjectMachineGrants(orgId, projectId))
  const explicit = grants.items.filter((item) => item.grant.source_kind === 'explicit')
  const count = explicit.length
  const [expanded, setExpanded] = useState(false)
  const expansionId = useId()
  const expansion = {
    id: expansionId,
    open: expanded,
    content: (
      <MachinePreviewList
        machines={explicit.slice(0, previewLimit).map((item) => item.machine)}
        isPending={grants.isPending}
        isError={grants.isError}
        emptyMessage="No individual machines shared with this project."
        viewAll={
          count > previewLimit && (
            <Link
              to="/projects/$projectId/machines/byo"
              params={{ projectId }}
              className={agentCardMoreLinkClass}
            >
              View all {formatCount(count)} machines →
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
      footer={<span className="truncate">Not part of any pool</span>}
      stats={
        <AgentCardStatToggle
          icon={Server}
          label={count === 1 ? 'machine' : 'machines'}
          value={grants.isPending || grants.isError ? undefined : formatCount(count)}
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
