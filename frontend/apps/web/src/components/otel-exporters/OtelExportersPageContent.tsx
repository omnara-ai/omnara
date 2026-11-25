import { OtelExportersSection } from '@/components/otel-exporters/OtelExportersSection'
import { useActiveOrg } from '@/lib/use-active-org'

/** The exporters page body, shared by the organization and project pages. */
export function OtelExportersPageContent({ projectId }: { projectId?: string }) {
  const { activeOrg } = useActiveOrg()
  return (
    <OtelExportersSection
      key={`${activeOrg.id}:${projectId ?? ''}`}
      orgId={activeOrg.id}
      projectId={projectId}
      heading={<h1 className="type-title">Traces</h1>}
    />
  )
}
