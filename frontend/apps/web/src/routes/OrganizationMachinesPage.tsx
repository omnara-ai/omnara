import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { MachinePoolsSection } from '@/components/overview/MachinePoolsSection'
import { MachinesSection } from '@/components/overview/MachinesSection'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationMachinesPage() {
  const { activeOrg } = useActiveOrg()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ label: activeOrg.name, to: '/' }, { label: 'Machines' }]} />
      <MachinePoolsSection />
      <MachinesSection />
    </div>
  )
}
