import { useCronTriggers } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { CreateCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { CronTriggersListContent } from '@/components/agents/CronTriggersSection'
import { appScheduleDefaults } from '@/components/apps/app-schedule-schema'
import { Button } from '@/components/ui/button'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

export function ProjectAppSchedules({
  orgId,
  projectId,
  app,
  canManage,
  hideWhenEmpty = false,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canManage: boolean
  hideWhenEmpty?: boolean
}) {
  const [creating, setCreating] = useState(false)
  const schedule = app.capabilities.schedule
  const query = useCronTriggers(orgId, projectId, {
    filters: { app_id: app.id },
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
            disabled={app.state !== 'active'}
            onClick={() => {
              setCreating(true)
            }}
          >
            Add schedule
          </Button>
        )}
      </div>
      <p className="text-muted-foreground text-sm">
        {schedule.description ?? 'Run this app’s scheduled action at chosen times.'}
      </p>
      {app.state !== 'active' && (
        <p className="text-muted-foreground text-sm">
          Connect this app before creating schedules or running scheduled actions.
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
      {canManage && app.state === 'active' && creating && (
        <CreateCronTriggerDialog
          open
          onOpenChange={setCreating}
          orgId={orgId}
          projectId={projectId}
          targetLabel={app.name}
          target={{
            type: 'app',
            app_id: app.id,
            settings: appScheduleDefaults(schedule.input_schema),
          }}
        />
      )}
    </section>
  )
}
