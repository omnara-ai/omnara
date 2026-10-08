import { MachineInventory } from '@/components/overview/MachineInventory'
import { MachinePoolsSection } from '@/components/overview/MachinePoolsSection'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationMachinesPage() {
  const { activeOrg } = useActiveOrg()
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      {/* Pools are manage-only, so members get the machines they can see instead. */}
      {canManageOrg(activeOrg.role) ? (
        <MachinePoolsSection />
      ) : (
        <MachineInventory
          orgId={activeOrg.id}
          filters={{}}
          description="Machines in this organization you can see. Organization admins manage pools."
          emptyMessage="No machines yet. Connect a machine you operate to run agents on it."
        />
      )}
    </div>
  )
}
