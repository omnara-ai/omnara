import { OtelExportersSection } from '@/components/otel-exporters/OtelExportersSection'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

/** The exporters page body, shared by the organization and project pages. */
export function OtelExportersPageContent({ projectId }: { projectId?: string }) {
  const { activeOrg } = useActiveOrg()
  const heading = <h1 className="type-title">Traces</h1>
  return canManageOrg(activeOrg.role) ? (
    <OtelExportersSection
      key={`${activeOrg.id}:${projectId ?? ''}`}
      orgId={activeOrg.id}
      projectId={projectId}
      heading={heading}
    />
  ) : (
    <section className="flex flex-col gap-4">
      {heading}
      <p className="text-muted-foreground text-sm">
        Only organization owners and admins can view and manage exporters.
      </p>
    </section>
  )
}
