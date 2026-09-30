import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { MachinePoolsSection } from '@/components/overview/MachinePoolsSection'
import { MachinesSection } from '@/components/overview/MachinesSection'

export function OrganizationMachinesPage() {
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ id: 'machines', label: 'Machines' }]} />
      <MachinePoolsSection />
      <MachinesSection />
    </div>
  )
}
