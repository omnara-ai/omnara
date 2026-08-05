import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { ConfiguredModelsSection } from '@/components/overview/ConfiguredModelsSection'
import { ModelProvidersSection } from '@/components/overview/ModelProvidersSection'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationModelsPage() {
  const { activeOrg } = useActiveOrg()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ label: activeOrg.name, to: '/' }, { label: 'Models' }]} />
      <ModelProvidersSection />
      <ConfiguredModelsSection />
    </div>
  )
}
