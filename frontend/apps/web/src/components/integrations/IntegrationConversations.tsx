import { useDeleteIntegrationSubscription, useIntegrationSubscriptions } from '@omnara/react'
import type { Integration } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'

import { integrationConversation } from './integrationConversation'

export function IntegrationConversations({
  orgId,
  projectId,
  integration,
  canManage,
  hideWhenEmpty = false,
}: {
  orgId: string
  projectId: string
  integration: Integration
  canManage: boolean
  hideWhenEmpty?: boolean
}) {
  const query = useIntegrationSubscriptions(orgId, projectId, integration.id)
  const subscriptions = useInfiniteQueryItems(query)
  const remove = useDeleteIntegrationSubscription(orgId, projectId, integration.id)
  if (hideWhenEmpty && !subscriptions.length && !query.isError) return null
  return (
    <section aria-label="Connected conversations" className="flex flex-col gap-3 text-sm">
      <h2 className="font-medium">Connected conversations</h2>
      <p className="text-muted-foreground">Conversations that send updates to your agents.</p>
      {integration.state !== 'active' && (
        <p className="text-muted-foreground">
          Forwarding is paused while this integration is disconnected. These connections are kept.
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
        <ul className="divide-y border-y">
          {subscriptions.map((subscription) => {
            const conversation = integrationConversation(integration, subscription)
            const agentName = subscription.agent_name || subscription.agent_id
            return (
              <li
                key={subscription.id}
                className="flex flex-wrap items-center justify-between gap-3 py-3"
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
                  <p className="text-muted-foreground">
                    <Link
                      className="text-foreground underline underline-offset-2"
                      to="/projects/$projectId/agents/$agentId/events"
                      params={{ projectId, agentId: subscription.agent_id }}
                    >
                      {agentName}
                    </Link>{' '}
                    · Added{' '}
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
                          `Stop forwarding ${conversation.label} to ${agentName}? Future updates from this conversation will no longer be forwarded to this agent. The agent, its sending tools and history are kept. To resume forwarding, reattach the conversation through the subscriptions API.`,
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
        !query.isError && <p className="text-muted-foreground">No connected conversations yet.</p>
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
