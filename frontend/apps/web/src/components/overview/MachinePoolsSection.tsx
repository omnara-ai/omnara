import { type MachinePoolListSort, useMachinePools, useMachines } from '@omnara/react'
import type { MachinePool } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useId, useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  agentCardLinkClass,
  AgentCardList,
  agentCardMoreLinkClass,
  AgentCardStatToggle,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { ManagedLogo, OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { Monitor, Server } from '@/components/icons'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { machinePoolProviderLabel } from '@/components/org/MachinePoolDialogState'
import { MachinePoolProviderLogo } from '@/components/org/MachinePoolProviderLogo'
import {
  MachinePoolActions,
  type MachinePoolDialog,
  MachinePoolDialogs,
} from '@/components/overview/MachinePoolActions'
import { formatPoolMachines, formatPoolResources } from '@/components/overview/machinePoolFormat'
import { MachinePreviewList } from '@/components/overview/MachinePreviewList'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatCount } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

const previewLimit = 5

export function MachinePoolsSection() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const list = useResourceList<MachinePoolListSort>('-created_at')
  const query = useMachinePools(activeOrg.id, { filters: list.apiFilters, sort: list.sort })
  const paged = usePagedQuery(query, list.queryKey)
  const [poolDialog, setPoolDialog] = useState<MachinePoolDialog>(null)

  const newPoolButton = () =>
    canManage ? (
      <Button
        size="sm"
        onClick={() => {
          setPoolDialog({ kind: 'create' })
        }}
      >
        New pool
      </Button>
    ) : undefined

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Machine pools"
          description="Configure pools of sandboxes for agents to use, or connect your own machines"
          guide={guides.machinePools}
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
              placeholder="Search pools by name…"
              showSearch
            />
          }
        >
          {newPoolButton()}
        </SearchHeader>
        <ByoMachinesCard orgId={activeOrg.id} />
        <AgentCardList
          items={paged.rows}
          getId={(pool) => pool.id}
          renderCard={(pool) => (
            <MachinePoolCard
              pool={pool}
              actions={
                canManage && (
                  <MachinePoolActions orgId={activeOrg.id} pool={pool} onOpen={setPoolDialog} />
                )
              }
            />
          )}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No machine pools yet. Pools provision the machines your agents run on."
          emptyAction={newPoolButton()}
        />
      </div>
      {canManage && (
        <MachinePoolDialogs
          orgId={activeOrg.id}
          dialog={poolDialog}
          onClose={() => {
            setPoolDialog(null)
          }}
        />
      )}
    </>
  )
}

function MachinePoolCard({ pool, actions }: { pool: MachinePool; actions: ReactNode }) {
  const { activeOrg } = useActiveOrg()
  const [expanded, setExpanded] = useState(false)
  const expansionId = useId()
  // Fetched only once opened: one extra row tells us whether "View all" is needed.
  const preview = useMachines(activeOrg.id, {
    filters: { machine_pool_id: pool.id },
    sort: '-updated_at',
    pageSize: previewLimit + 1,
    enabled: expanded,
  })
  const firstPage = preview.data?.pages[0]?.data ?? []
  const resources = formatPoolResources(pool)
  const expansion = {
    id: expansionId,
    open: expanded,
    content: (
      <MachinePreviewList
        machines={firstPage.slice(0, previewLimit)}
        isPending={preview.isPending}
        isError={preview.isError}
        emptyMessage="No machines yet. The pool provisions machines as agents need them."
        viewAll={
          firstPage.length > previewLimit && (
            <Link
              to="/machines/pools/$poolId"
              params={{ poolId: pool.id }}
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
          <ManagedLogo managed={pool.management_kind === 'cluster'}>
            <MachinePoolProviderLogo provider={pool.provider} />
          </ManagedLogo>
        </AgentCardGlyph>
      }
      title={
        <>
          <Link
            to="/machines/pools/$poolId"
            params={{ poolId: pool.id }}
            className={agentCardLinkClass}
          >
            {pool.name}
          </Link>
          {pool.management_kind === 'cluster' && <OmnaraManagedTag />}
        </>
      }
      subtitle={<PoolSubtitle pool={pool} />}
      meta={
        <>
          <AgentCardTime label="Updated" value={pool.updated_at} />
          {actions}
        </>
      }
      footer={resources && <span className="truncate tabular-nums">{resources}</span>}
      stats={
        <AgentCardStatToggle
          icon={Server}
          label={pool.max_total_machines === 1 ? 'machine' : 'machines'}
          value={formatPoolMachines(pool)}
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

export function PoolSubtitle({ pool }: { pool: MachinePool }) {
  return (
    <>
      <span className="shrink-0">{machinePoolProviderLabel(pool.provider)}</span>
      {pool.description && (
        <>
          <span aria-hidden="true">·</span>
          <span className="truncate">{pool.description}</span>
        </>
      )}
    </>
  )
}

function ByoMachinesCard({ orgId }: { orgId: string }) {
  // One small page: enough for the preview, and one extra row tells us whether there are more.
  const preview = useMachines(orgId, {
    filters: { source_kind: 'byo' },
    sort: '-updated_at',
    pageSize: previewLimit + 1,
  })
  const firstPage = preview.data?.pages[0]?.data ?? []
  const hasMore = firstPage.length > previewLimit
  const machines = firstPage.slice(0, previewLimit)
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
        emptyMessage="No machines connected yet. Connect a machine you operate to run agents on it."
        viewAll={
          hasMore && (
            <Link to="/machines/byo" className={agentCardMoreLinkClass}>
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
        <Link to="/machines/byo" className={agentCardLinkClass}>
          BYO Machines
        </Link>
      }
      subtitle={<span className="truncate">Machines you connect and run yourself</span>}
      meta={machines[0] && <AgentCardTime label="Updated" value={machines[0].updated_at} />}
      footer={<span className="truncate">Not part of any pool</span>}
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
