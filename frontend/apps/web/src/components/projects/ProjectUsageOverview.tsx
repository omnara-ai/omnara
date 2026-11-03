import { Link } from '@tanstack/react-router'

import { ArrowRight } from '@/components/icons'
import { panelHintClass } from '@/components/overview/CodeBlock'
import { UsageOverview } from '@/components/overview/UsageOverview'
import { cn } from '@/lib/utils'

export function ProjectUsageOverview({ orgId, projectId }: { orgId: string; projectId: string }) {
  return (
    <UsageOverview
      orgId={orgId}
      filters={{ projectIDs: [projectId] }}
      reportLink={
        <Link
          to="/projects/$projectId/usage"
          params={{ projectId }}
          className={cn(panelHintClass, 'inline-flex hover:underline')}
        >
          Usage report
          <ArrowRight className="size-3.5" aria-hidden="true" />
        </Link>
      }
    />
  )
}
