import { AgentCard, AgentCardGlyph } from '@/components/agents/AgentCardList'
import { Monitor } from '@/components/icons'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { MachineInventory } from '@/components/overview/MachineInventory'
import { useActiveOrg } from '@/lib/use-active-org'

export function ByoMachinesPage() {
  const { activeOrg } = useActiveOrg()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'machines', label: 'Machines', to: '/machines' },
          { id: 'byo', label: 'BYO Machines' },
        ]}
      />
      <AgentCard
        icon={
          <AgentCardGlyph>
            <Monitor aria-hidden="true" />
          </AgentCardGlyph>
        }
        title={<h1 className="truncate font-medium">BYO Machines</h1>}
        subtitle={<span className="truncate">Machines you connect and run yourself</span>}
        meta={null}
      />
      <MachineInventory
        orgId={activeOrg.id}
        filters={{ source_kind: 'byo' }}
        description="Machines that aren't part of any pool"
        emptyMessage="No machines connected yet. Connect a machine you operate to run agents on it."
      />
    </div>
  )
}
