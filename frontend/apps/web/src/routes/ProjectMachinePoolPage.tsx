import {
  useDeleteProjectMachinePoolGrant,
  useMachinePool,
  useProjectMachinePoolGrants,
  useProjectMachines,
} from '@omnara/react'
import type { ProjectMachinePoolGrantListItem } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  AgentCardStat,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { ManagedLogo, OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { Server } from '@/components/icons'
import { SectionTitle } from '@/components/layout/SectionTitle'
import { machinePoolProviderLabel } from '@/components/org/MachinePoolDialogState'
import { MachinePoolProviderLogo } from '@/components/org/MachinePoolProviderLogo'
import { formatPoolMachines } from '@/components/overview/machinePoolFormat'
import { MachinesTable } from '@/components/overview/MachinesTable'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { EditMachinePoolGrantDialog } from '@/components/projects/EditMachinePoolGrantDialog'
import { poolGrantOverrides } from '@/components/projects/grant-override-diffs'
import { OverrideChip, OverrideList } from '@/components/projects/GrantOverrides'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useAllPages } from '@/hooks/use-all-pages'
import { formatCount } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function ProjectMachinePoolPage() {
  const { activeOrg } = useActiveOrg()
  const { projectId = '', poolId = '' } = useParams({ strict: false })
  const grants = useAllPages(useProjectMachinePoolGrants(activeOrg.id, projectId))
  const item = grants.items.find((candidate) => candidate.grant.machine_pool_id === poolId)

  return (
    <ProjectPageFrame
      crumbs={[
        {
          id: 'machines',
          label: 'Machines',
          to: '/projects/$projectId/machines',
          params: { projectId },
        },
        { id: 'pool', label: item?.machine_pool.name ?? 'Machine pool' },
      ]}
    >
      {({ project }) =>
        !project?.access.can_read ? (
          <p className="text-muted-foreground text-sm">
            You don&rsquo;t have permission to view shared machines in this project.
          </p>
        ) : item ? (
          <SharedPoolView
            orgId={activeOrg.id}
            projectId={projectId}
            item={item}
            canManageAccess={project.access.can_manage_access}
          />
        ) : grants.isPending ? (
          <Skeleton className="h-[7.25rem] rounded-xl" />
        ) : (
          <Empty className="rounded-xl border">
            <EmptyHeader>
              <EmptyDescription>
                This machine pool isn&rsquo;t shared with the project.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button asChild size="sm" variant="ghost">
                <Link to="/projects/$projectId/machines" params={{ projectId }}>
                  Back to machines
                </Link>
              </Button>
            </EmptyContent>
          </Empty>
        )
      }
    </ProjectPageFrame>
  )
}

function SharedPoolView({
  orgId,
  projectId,
  item,
  canManageAccess,
}: {
  orgId: string
  projectId: string
  item: ProjectMachinePoolGrantListItem
  canManageAccess: boolean
}) {
  const { activeOrg } = useActiveOrg()
  const canManageGrants = canManageAccess && canManageOrg(activeOrg.role)
  const summary = item.machine_pool
  // The org pool supplies the defaults the project overrides; it may be hidden from the viewer.
  const { data: pool } = useMachinePool(orgId, summary.id)
  const deleteGrant = useDeleteProjectMachinePoolGrant(orgId, projectId)
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)
  const overrides = poolGrantOverrides(item.grant, pool)
  const description = item.grant.description || summary.description

  return (
    <>
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
            <h1 className="truncate font-medium">{summary.name}</h1>
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
          <>
            <AgentCardTime label="Updated" value={item.grant.updated_at} />
            {canManageGrants && (
              <ResourceRowActions
                deleteLabel="Stop sharing"
                onEdit={() => {
                  setEditing(true)
                }}
                onDelete={() => {
                  if (!window.confirm(`Stop sharing ${summary.name} with this project?`)) return
                  deleteGrant.mutate(item.grant.id, {
                    onSuccess: () => {
                      void navigate({ to: '/projects/$projectId/machines', params: { projectId } })
                    },
                  })
                }}
              />
            )}
          </>
        }
        footer={
          pool && (
            <span className="truncate tabular-nums">
              Org pool: {formatPoolMachines(pool)} in use
            </span>
          )
        }
        stats={
          <ProjectPoolMachinesStat
            orgId={orgId}
            projectId={projectId}
            poolId={summary.id}
            quota={item.grant.max_total_machines ?? pool?.max_total_machines}
          />
        }
      />
      {overrides.length > 0 && (
        <section className="flex flex-col gap-3">
          <SectionTitle title="Project overrides" />
          <div className="rounded-xl border px-4 py-3">
            <OverrideList overrides={overrides} />
          </div>
        </section>
      )}
      <MachinesTable
        orgId={orgId}
        projectId={projectId}
        filters={{ machine_pool_id: summary.id }}
        description="Machines this pool has provisioned for agents in this project"
        emptyMessage="No machines yet. The pool provisions machines as this project's agents need them."
      />
      {canManageGrants && editing && (
        <EditMachinePoolGrantDialog
          key={item.grant.id}
          open
          onOpenChange={setEditing}
          orgId={orgId}
          projectId={projectId}
          item={item}
        />
      )}
    </>
  )
}

/** Machines the pool has running for this project, against the project's quota when set. */
function ProjectPoolMachinesStat({
  orgId,
  projectId,
  poolId,
  quota,
}: {
  orgId: string
  projectId: string
  poolId: string
  quota: number | null | undefined
}) {
  const machines = useAllPages(
    useProjectMachines(orgId, projectId, { filters: { machine_pool_id: poolId } }),
  )
  const inUse = machines.isPending || machines.isError ? undefined : machines.items.length
  let value: string | undefined
  if (inUse !== undefined) {
    value = quota == null ? formatCount(inUse) : `${formatCount(inUse)} / ${formatCount(quota)}`
  }
  return <AgentCardStat icon={Server} label="in this project" value={value} />
}
