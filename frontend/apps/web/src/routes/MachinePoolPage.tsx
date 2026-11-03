import { useMachinePool } from '@omnara/react'
import type { MachinePool } from '@omnara/sdk'
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
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { MachinePoolProviderLogo } from '@/components/org/MachinePoolProviderLogo'
import {
  MachinePoolActions,
  type MachinePoolDialog,
  MachinePoolDialogs,
} from '@/components/overview/MachinePoolActions'
import { formatPoolMachines, formatPoolResources } from '@/components/overview/machinePoolFormat'
import { PoolSubtitle } from '@/components/overview/MachinePoolsSection'
import { MachinesTable } from '@/components/overview/MachinesTable'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function MachinePoolPage() {
  const { activeOrg } = useActiveOrg()
  const { poolId = '' } = useParams({ strict: false })
  const poolQuery = useMachinePool(activeOrg.id, poolId)
  const pool = poolQuery.data

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'machines', label: 'Machines', to: '/machines' },
          { id: 'pool', label: pool?.name ?? 'Machine pool' },
        ]}
      />
      {pool ? (
        <PoolView pool={pool} />
      ) : poolQuery.isError ? (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>Couldn&rsquo;t load this machine pool.</EmptyDescription>
          </EmptyHeader>
          <EmptyContent className="flex-row justify-center gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                void poolQuery.refetch()
              }}
            >
              Retry
            </Button>
            <Button asChild size="sm" variant="ghost">
              <Link to="/machines">Back to machines</Link>
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <Skeleton className="h-[7.25rem] rounded-xl" />
      )}
    </div>
  )
}

function PoolView({ pool }: { pool: MachinePool }) {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const navigate = useNavigate()
  const [dialog, setDialog] = useState<MachinePoolDialog>(null)
  const resources = formatPoolResources(pool)

  return (
    <>
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
            <h1 className="truncate font-medium">{pool.name}</h1>
            {pool.management_kind === 'cluster' && <OmnaraManagedTag />}
          </>
        }
        subtitle={<PoolSubtitle pool={pool} />}
        meta={
          <>
            <AgentCardTime label="Updated" value={pool.updated_at} />
            {canManage && (
              <MachinePoolActions
                orgId={activeOrg.id}
                pool={pool}
                onOpen={setDialog}
                onDeleted={() => {
                  void navigate({ to: '/machines' })
                }}
              />
            )}
          </>
        }
        footer={resources && <span className="truncate tabular-nums">{resources}</span>}
        stats={
          <AgentCardStat icon={Server} label="machines in use" value={formatPoolMachines(pool)} />
        }
      />
      <MachinesTable
        orgId={activeOrg.id}
        filters={{ machine_pool_id: pool.id }}
        description="Machines this pool has provisioned for agents"
        emptyMessage="No machines yet. The pool provisions machines as agents need them."
      />
      {canManage && (
        <MachinePoolDialogs
          orgId={activeOrg.id}
          dialog={dialog}
          onClose={() => {
            setDialog(null)
          }}
        />
      )}
    </>
  )
}
