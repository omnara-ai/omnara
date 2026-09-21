import { useCronTriggers } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { CreateCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { CronTriggersListContent } from '@/components/agents/CronTriggersSection'
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
  const query = useCronTriggers(orgId, projectId, { filters: { app_id: app.id } })
  const schedules = useInfiniteQueryItems(query)
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
        Start a fresh agent in a new channel thread on each run. Schedules work independently of
        mentions.
      </p>
      {app.state !== 'active' && (
        <p className="text-muted-foreground text-sm">
          Connect this app before creating schedules or running scheduled agents.
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
            type: 'app_launch',
            app_id: app.id,
            agent_profile_id: '',
            destination: { channel_id: '' },
            opening_message_template: '{{.trigger.name}} — {{.trigger.local_date}}',
          }}
        />
      )}
    </section>
  )
}
