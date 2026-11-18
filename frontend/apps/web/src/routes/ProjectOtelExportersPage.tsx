import { OtelExportersPageContent } from '@/components/otel-exporters/OtelExportersPageContent'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export default function ProjectOtelExportersPage() {
  return (
    <ProjectPageFrame>
      {({ projectId }) => <OtelExportersPageContent projectId={projectId} />}
    </ProjectPageFrame>
  )
}
