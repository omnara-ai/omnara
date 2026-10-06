import { ConfiguredModelsSection } from '@/components/overview/ConfiguredModelsSection'
import { ModelProvidersSection } from '@/components/overview/ModelProvidersSection'

export function OrganizationModelsPage() {
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <ModelProvidersSection />
      <ConfiguredModelsSection />
    </div>
  )
}
