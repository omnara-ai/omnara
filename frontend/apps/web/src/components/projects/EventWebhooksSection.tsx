import { useDeleteEventWebhook, useEventWebhooks } from '@omnara/react'
import type { EventWebhook } from '@omnara/sdk'
import { useState } from 'react'

import { PencilIcon, PlusIcon, Trash2 } from '@/components/icons'
import { EventWebhookDialog } from '@/components/projects/EventWebhookDialog'
import { EventWebhookScopeLabel } from '@/components/projects/EventWebhookScopeField'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { errorMessage } from '@/lib/submit-status'

export function EventWebhooksSection({
  orgId,
  projectId,
  canManage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
}) {
  const query = useEventWebhooks(orgId, projectId)
  const webhooks = useInfiniteQueryItems(query)
  const remove = useDeleteEventWebhook(orgId, projectId)
  const [editing, setEditing] = useState<EventWebhook | 'new' | null>(null)
  return (
    <section className="flex min-w-0 flex-col gap-3">
      {canManage && (
        <Button
          size="sm"
          variant="outline"
          className="self-end"
          onClick={() => {
            setEditing('new')
          }}
        >
          <PlusIcon /> Add webhook
        </Button>
      )}
      {webhooks.length > 0 && (
        <ul className="flex flex-col divide-y">
          {webhooks.map((webhook) => (
            <li key={webhook.id} className="flex min-w-0 items-center justify-between gap-3 py-3">
              <div className="flex min-w-0 flex-col gap-1">
                <p className="truncate text-sm font-medium" title={webhook.url}>
                  {webhook.url}
                </p>
                <div className="text-muted-foreground text-sm">
                  <EventWebhookScopeLabel
                    orgId={orgId}
                    projectId={projectId}
                    scope={webhook.scope}
                  />
                </div>
                <p
                  className="text-muted-foreground truncate text-xs"
                  title={webhook.event_types.join(', ')}
                >
                  {webhook.event_types.join(', ')} ·{' '}
                  {webhook.signing_secret_id ? 'Signed' : 'Unsigned'}
                </p>
              </div>
              {canManage && (
                <div className="flex shrink-0 items-center">
                  <Button
                    size="icon"
                    variant="ghost"
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
          ))}
        </ul>
      )}
      {query.isPending && <Spinner className="text-muted-foreground size-4" />}
      {query.isError && (
        <div className="flex items-center gap-2">
          <p role="alert" className="text-destructive text-sm">
            Could not load all webhooks.
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              void query.refetch()
            }}
          >
            Retry
          </Button>
        </div>
      )}
      {!query.isPending && !query.isError && webhooks.length === 0 && (
        <p className="text-muted-foreground text-sm">No webhooks.</p>
      )}
      {query.hasNextPage && (
        <Button
          variant="outline"
          className="self-start"
          loading={query.isFetchingNextPage}
          onClick={() => {
            void query.fetchNextPage()
          }}
        >
          Load more webhooks
        </Button>
      )}
      {remove.isError && (
        <p role="alert" className="text-destructive text-sm">
          {errorMessage(remove.error, 'Could not delete webhook')}
        </p>
      )}
      {canManage && editing && (
        <EventWebhookDialog
          orgId={orgId}
          projectId={projectId}
          webhook={editing === 'new' ? undefined : editing}
          onClose={() => {
            setEditing(null)
          }}
        />
      )}
    </section>
  )
}
