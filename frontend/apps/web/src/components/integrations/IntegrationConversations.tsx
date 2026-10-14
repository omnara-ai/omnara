import { useDeleteIntegrationSubscription, useIntegrationSubscriptions } from '@omnara/react'
import type { Integration, IntegrationSubscription } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { AgentCard, agentCardLinkClass, AgentCardTime } from '@/components/agents/AgentCardList'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { agentIcon } from '@/lib/agent-icon'
import { errorMessage } from '@/lib/submit-status'

import { integrationConversation } from './integrationConversation'

export function IntegrationConversations({
  orgId,
  projectId,
  integration,
  canManage,
  hideWhenEmpty = false,
  titled = true,
}: {
  orgId: string
  projectId: string
  integration: Integration
  canManage: boolean
  hideWhenEmpty?: boolean
  /** False where a tab already names the section. */
  titled?: boolean
}) {
  const query = useIntegrationSubscriptions(orgId, projectId, integration.id)
  const subscriptions = useInfiniteQueryItems(query)
  const remove = useDeleteIntegrationSubscription(orgId, projectId, integration.id)
  if (hideWhenEmpty && !subscriptions.length && !query.isError) return null
  return (
    <section aria-label="Connected conversations" className="flex flex-col gap-3 text-sm">
      {titled && <h2 className="font-medium">Connected conversations</h2>}
      <p className="text-muted-foreground">Conversations that send updates to your agents.</p>
      {integration.state !== 'active' && (
        <p className="text-muted-foreground">
          Forwarding is paused while this integration is disconnected. These connections are kept.
        </p>
      )}
      <ConversationsStatus query={query} removeError={remove.isError ? remove.error : null} />
      {subscriptions.length > 0 ? (
        <ul className="flex flex-col gap-3">
          {subscriptions.map((subscription) => (
            <li key={subscription.id}>
              <ConversationCard
                projectId={projectId}
                integration={integration}
                subscription={subscription}
                canManage={canManage}
                stopping={remove.isPending && remove.variables === subscription.id}
                busy={remove.isPending}
                onStop={() => {
                  remove.mutate(subscription.id)
                }}
              />
            </li>
          ))}
        </ul>
      ) : (
        !query.isPending &&
        !query.isError && <p className="text-muted-foreground">No connected conversations yet.</p>
      )}
      <LoadMoreConversations query={query} />
    </section>
  )
}

/** A forwarded conversation, shown as the agent it reaches, like the project's agent cards. */
function ConversationCard({
  projectId,
  integration,
  subscription,
  canManage,
  stopping,
  busy,
  onStop,
}: {
  projectId: string
  integration: Integration
  subscription: IntegrationSubscription
  canManage: boolean
  stopping: boolean
  busy: boolean
  onStop: () => void
}) {
  const conversation = integrationConversation(integration, subscription)
  const agentName = subscription.agent_name || subscription.agent_id
  return (
    <AgentCard
      icon={agentIcon(undefined, subscription.agent_id)}
      title={
        <Link
          to="/projects/$projectId/agents/$agentId/events"
          params={{ projectId, agentId: subscription.agent_id }}
          className={agentCardLinkClass}
        >
          {agentName}
        </Link>
      }
      subtitle={
        <>
          {conversation.href ? (
            <a
              href={conversation.href}
              target="_blank"
              rel="noreferrer"
              className="hover:text-foreground relative truncate underline underline-offset-2"
            >
              {conversation.label}
            </a>
          ) : (
            <span className="truncate">{conversation.label}</span>
          )}
          <span aria-hidden="true">·</span>
          <span className="shrink-0">
            Added <AgentCardTime label="Added" value={subscription.created_at} />
          </span>
        </>
      }
      meta={
        canManage && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            aria-label={`Stop forwarding ${conversation.label} to ${agentName}`}
            onClick={() => {
              if (
                !window.confirm(
                  `Stop forwarding ${conversation.label} to ${agentName}? Future updates from this conversation will no longer be forwarded to this agent. The agent, its sending tools and history are kept. To resume forwarding, reattach the conversation through the subscriptions API.`,
                )
              )
                return
              onStop()
            }}
          >
            {stopping ? 'Stopping…' : 'Stop forwarding'}
          </Button>
        )
      }
    />
  )
}

type SubscriptionsQuery = ReturnType<typeof useIntegrationSubscriptions>

function ConversationsStatus({
  query,
  removeError,
}: {
  query: SubscriptionsQuery
  removeError: unknown
}) {
  return (
    <>
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
      {removeError !== null && (
        <p role="alert" className="text-destructive">
          {errorMessage(removeError, 'Could not stop forwarding. Try again.')}
        </p>
      )}
    </>
  )
}

function LoadMoreConversations({ query }: { query: SubscriptionsQuery }) {
  if (!query.hasNextPage || query.isFetchNextPageError) return null
  return (
    <Button
      variant="outline"
      size="sm"
      className="self-start"
      disabled={query.isFetching}
      onClick={() => void query.fetchNextPage()}
    >
      {query.isFetchingNextPage ? 'Loading conversations…' : 'Load more conversations'}
    </Button>
  )
}
