import { useCronTriggers } from '@omnara/react'
import type { ProjectIntegration } from '@omnara/sdk'
import { useState } from 'react'

import { CreateCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { CronTriggersListContent } from '@/components/agents/CronTriggersSection'
import { integrationScheduleDefaults } from '@/components/integrations/integration-schedule-schema'
import { Button } from '@/components/ui/button'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

export function ProjectIntegrationSchedules({
  orgId,
  projectId,
  integration,
  canManage,
  hideWhenEmpty = false,
}: {
  orgId: string
  projectId: string
  integration: ProjectIntegration
  canManage: boolean
  hideWhenEmpty?: boolean
}) {
  const [creating, setCreating] = useState(false)
  const schedule = integration.capabilities.schedule
  const query = useCronTriggers(orgId, projectId, {
    filters: { integration_id: integration.id },
    enabled: Boolean(schedule),
  })
  const schedules = useInfiniteQueryItems(query)
  if (!schedule) return null
  if (hideWhenEmpty && !schedules.length && !query.isError) return null
  return (
    <section aria-label="Schedules" className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-sm font-medium">Schedules</h2>
        {canManage && (
          <Button
            size="sm"
            variant="outline"
            disabled={integration.state !== 'active'}
            onClick={() => {
              setCreating(true)
            }}
          >
            Add schedule
          </Button>
        )}
      </div>
      <p className="text-muted-foreground text-sm">
        {schedule.description ?? 'Run this integration’s scheduled action at chosen times.'}
      </p>
      {integration.state !== 'active' && (
        <p className="text-muted-foreground text-sm">
          Connect this integration before creating schedules or running scheduled actions.
        </p>
      )}
      <CronTriggersListContent
        orgId={orgId}
        projectId={projectId}
        canManage={canManage}
        query={query}
        emptyMessage="No schedules yet."
        plain
      />
      {canManage && integration.state === 'active' && creating && (
        <CreateCronTriggerDialog
          open
          onOpenChange={setCreating}
          orgId={orgId}
          projectId={projectId}
          targetLabel={integration.name}
          target={{
            type: 'integration',
            integration_id: integration.id,
            settings: integrationScheduleDefaults(schedule.input_schema),
          }}
        />
      )}
    </section>
  )
}
