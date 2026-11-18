import { OtelExportersSection } from '@/components/otel-exporters/OtelExportersSection'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

/** The exporters page body, shared by the organization and project pages. */
export function OtelExportersPageContent({ projectId }: { projectId?: string }) {
  const { activeOrg } = useActiveOrg()
  return (
    <section className="flex flex-col gap-4">
      <div>
        <h1 className="type-title">Traces</h1>
        <p className="text-muted-foreground text-sm">
          {projectId
            ? 'Send agent turns in this project to an OpenTelemetry endpoint as traces.'
            : 'Send agent turns to an OpenTelemetry endpoint as traces, for the whole organization, one project, or one agent.'}
        </p>
      </div>
      {canManageOrg(activeOrg.role) ? (
        <OtelExportersSection
          key={`${activeOrg.id}:${projectId ?? ''}`}
          orgId={activeOrg.id}
          projectId={projectId}
        />
      ) : (
        <p className="text-muted-foreground text-sm">
          Only organization owners and admins can view and manage exporters.
        </p>
      )}
    </section>
  )
}
