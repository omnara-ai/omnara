import { useDeleteEventWebhook, useEventWebhooks } from '@omnara/react'
import type { EventWebhook, EventWebhookScope } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { EventWebhookDialog } from '@/components/agents/EventWebhookDialog'
import { PencilIcon, PlusIcon, Trash2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { SidebarGroupLabel } from '@/components/ui/sidebar'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

interface EventWebhooksSectionProps {
  orgId: string
  projectId: string
  scope: EventWebhookScope
  canManage: boolean
  profileId?: string
  sidebar?: boolean
}

export function EventWebhooksSection(props: EventWebhooksSectionProps) {
  const { orgId, projectId, scope, profileId } = props
  const inheritsProfile = scope.kind === 'agent' && !!profileId
  const inheritsProject = scope.kind !== 'project'
  const direct = useEventWebhooks(orgId, projectId, scope)
  const profile = useEventWebhooks(
    orgId,
    projectId,
    { kind: 'profile', agent_profile_id: profileId ?? '' },
    { enabled: inheritsProfile },
  )
  const project = useEventWebhooks(
    orgId,
    projectId,
    { kind: 'project' },
    { enabled: inheritsProject },
  )
  return (
    <EventWebhooksList
      key={
        scope.kind === 'agent'
          ? scope.agent_id
          : scope.kind === 'profile'
            ? scope.agent_profile_id
            : projectId
      }
      {...props}
      queries={[
        direct,
        ...(inheritsProfile ? [profile] : []),
        ...(inheritsProject ? [project] : []),
      ]}
    />
  )
}

function EventWebhooksList({
  queries,
  ...props
}: EventWebhooksSectionProps & { queries: ReturnType<typeof useEventWebhooks>[] }) {
  const { orgId, projectId, scope, canManage, sidebar = false } = props
  const webhooks = queries.flatMap((query) => query.data?.data ?? [])
  const remove = useDeleteEventWebhook(orgId, projectId)
  const [editing, setEditing] = useState<EventWebhook | 'new' | null>(null)
  const addButton = canManage && (
    <Button
      size="sm"
      variant="ghost"
      className={sidebar ? 'text-muted-foreground h-9 px-2 sm:h-7' : 'self-end'}
      aria-label="Add webhook"
      onClick={() => {
        setEditing('new')
      }}
    >
      <PlusIcon /> {sidebar ? 'Add' : 'Add webhook'}
    </Button>
  )

  return (
    <>
      {sidebar && (
        <div className="flex items-center justify-between">
          <SidebarGroupLabel className="px-0 text-sm">Webhooks</SidebarGroupLabel>
          {addButton}
        </div>
      )}
      <section className="flex min-w-0 flex-col gap-2">
        {!sidebar && addButton}
        {webhooks.length > 0 && (
          <ul
            className={cn('flex flex-col divide-y', sidebar && 'bg-background rounded-md border')}
          >
            {webhooks.map((webhook) => {
              const inherited = webhook.scope.kind !== scope.kind
              const scopeLabel = {
                project: 'Project webhook',
                profile: 'Profile webhook',
                agent: 'Agent webhook',
              }[webhook.scope.kind]
              return (
                <li
                  key={webhook.id}
                  className={cn(
                    'flex min-w-0 items-center justify-between gap-3 py-2',
                    sidebar && 'px-3',
                  )}
                >
                  <div className="flex min-w-0 flex-col gap-0.5">
                    <p className="truncate text-sm font-medium" title={webhook.url}>
                      {webhook.url}
                    </p>
                    <p
                      className="text-muted-foreground truncate text-xs"
                      title={webhook.event_types.join(', ')}
                    >
                      {webhook.event_types.join(', ')}
                    </p>
                    <p className="text-muted-foreground text-xs">
                      {inherited && webhook.scope.kind === 'profile' ? (
                        <Link
                          to="/projects/$projectId/agent-profiles/$profileId/webhooks"
                          params={{ projectId, profileId: webhook.scope.agent_profile_id }}
                          className="hover:underline"
                        >
                          {scopeLabel}
                        </Link>
                      ) : (
                        scopeLabel
                      )}
                    </p>
                  </div>
                  {canManage && !inherited && (
                    <div className="flex shrink-0 items-center">
                      <Button
                        size="icon"
                        variant="ghost"
                        className="text-muted-foreground size-8 sm:size-7"
                        aria-label={'Edit webhook ' + webhook.url}
                        onClick={() => {
                          setEditing(webhook)
                        }}
                      >
                        <PencilIcon />
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="text-muted-foreground size-8 sm:size-7"
                        aria-label={'Delete webhook ' + webhook.url}
                        disabled={remove.isPending}
                        onClick={() => {
                          if (window.confirm('Delete webhook ' + webhook.url + '?'))
                            remove.mutate(webhook.id)
                        }}
                      >
                        <Trash2 />
                      </Button>
                    </div>
                  )}
                </li>
              )
            })}
          </ul>
        )}
        <EventWebhooksStatus queries={queries} empty={webhooks.length === 0} sidebar={sidebar} />
        {remove.isError && (
          <p role="alert" className="text-destructive text-sm">
            {errorMessage(remove.error, 'Could not delete webhook')}
          </p>
        )}
        {canManage && editing && (
          <EventWebhookDialog
            orgId={orgId}
            projectId={projectId}
            scope={scope}
            webhook={editing === 'new' ? undefined : editing}
            onClose={() => {
              setEditing(null)
            }}
          />
        )}
      </section>
    </>
  )
}

function EventWebhooksStatus({
  queries,
  empty,
  sidebar,
}: {
  queries: ReturnType<typeof useEventWebhooks>[]
  empty: boolean
  sidebar: boolean
}) {
  const isPending = queries.some((query) => query.isPending)
  const isError = queries.some((query) => query.isError)
  return (
    <>
      {isError && (
        <div className="flex items-center gap-2">
          <p role="alert" className="text-destructive text-sm">
            Could not load all webhooks.
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              for (const query of queries) void query.refetch()
            }}
          >
            Retry
          </Button>
        </div>
      )}
      {isPending && <Spinner className="text-muted-foreground size-4" />}
      {!isPending && !isError && empty && (
        <p className={cn('text-muted-foreground text-sm', sidebar && 'truncate py-1.5')}>
          {sidebar ? 'No webhooks' : 'No webhooks.'}
        </p>
      )}
    </>
  )
}
