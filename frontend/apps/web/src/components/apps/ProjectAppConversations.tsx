import { useAppSubscriptions, useDeleteAppSubscription } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'

import { appConversation } from './appConversation'

export function ProjectAppConversations({
  orgId,
  projectId,
  app,
  canManage,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  canManage: boolean
}) {
  const query = useAppSubscriptions(orgId, projectId, app.id)
  const subscriptions = useInfiniteQueryItems(query)
  const remove = useDeleteAppSubscription(orgId, projectId, app.id)
  return (
    <section aria-label="Conversations" className="flex flex-col gap-3 text-sm">
      <h2 className="font-medium">Conversations</h2>
      <p className="text-muted-foreground">
        Forward incoming events to these agents. Stopping forwarding keeps the agent, its tools and
        history. A stopped selected thread will not launch a replacement agent on the next mention.
        Reattach through the subscriptions API to resume forwarding.
      </p>
      {app.state !== 'active' && (
        <p className="text-muted-foreground">
          Forwarding is paused while this app is disconnected. Existing subscriptions are kept.
        </p>
      )}
      {query.isPending && (
        <p role="status" className="text-muted-foreground flex items-center gap-2">
          <Spinner className="size-4" /> Loading conversations…
        </p>
      )}
      {query.isError && (
        <div role="alert" className="flex flex-wrap items-center gap-3">
          {query.isFetchNextPageError
            ? 'Could not load more conversations.'
            : 'Could not load conversations.'}
          <Button
            variant="outline"
            size="sm"
            disabled={query.isFetching}
            onClick={() =>
              void (query.isFetchNextPageError ? query.fetchNextPage() : query.refetch())
            }
          >
            Retry conversations
          </Button>
        </div>
      )}
      {remove.isError && (
        <p role="alert" className="text-destructive">
          {errorMessage(remove.error, 'Could not stop forwarding. Try again.')}
        </p>
      )}
      {subscriptions.length > 0 ? (
        <ul className="divide-y rounded-md border">
          {subscriptions.map((subscription) => {
            const conversation = appConversation(app, subscription)
            const agentName = subscription.agent_name || subscription.agent_id
            return (
              <li
                key={subscription.id}
                className="flex flex-wrap items-center justify-between gap-3 px-3 py-3"
              >
                <div className="flex min-w-0 flex-col gap-1 break-words">
                  {conversation.href ? (
                    <a
                      href={conversation.href}
                      target="_blank"
                      rel="noreferrer"
                      className="underline underline-offset-2"
                    >
                      {conversation.label}
                    </a>
                  ) : (
                    <p>{conversation.label}</p>
                  )}
                  <p>
                    Forwarding to{' '}
                    <Link
                      className="underline underline-offset-2"
                      to="/projects/$projectId/agents/$agentId/events"
                      params={{ projectId, agentId: subscription.agent_id }}
                    >
                      {agentName}
                    </Link>
                  </p>
                  <p className="text-muted-foreground text-xs">
                    {subscription.type} · {subscription.events.join(', ')} · Added{' '}
                    <time dateTime={subscription.created_at}>
                      {formatDateTime(subscription.created_at)}
                    </time>
                  </p>
                </div>
                {canManage && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={remove.isPending}
                    aria-label={`Stop forwarding ${conversation.label} to ${agentName}`}
                    onClick={() => {
                      if (
                        !window.confirm(
                          `Stop forwarding ${conversation.label} to ${agentName}? The agent, sending tools and history are kept. Reattach through the subscriptions API to resume.`,
                        )
                      )
                        return
                      remove.mutate(subscription.id)
                    }}
                  >
                    {remove.isPending && remove.variables === subscription.id
                      ? 'Stopping…'
                      : 'Stop forwarding'}
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      ) : (
        !query.isPending &&
        !query.isError && (
          <p className="text-muted-foreground rounded-md border border-dashed p-4">
            No conversations yet. Launch an agent from this app or attach a conversation through the
            subscriptions API.
          </p>
        )
      )}
      {query.hasNextPage && !query.isFetchNextPageError && (
        <Button
          variant="outline"
          size="sm"
          className="self-start"
          disabled={query.isFetching}
          onClick={() => void query.fetchNextPage()}
        >
          {query.isFetchingNextPage ? 'Loading conversations…' : 'Load more conversations'}
        </Button>
      )}
    </section>
  )
}
