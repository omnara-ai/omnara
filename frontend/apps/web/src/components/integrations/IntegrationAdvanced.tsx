import type { Integration } from '@omnara/sdk'

import { ChevronRightIcon } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

import { useIntegrationSetupURLs } from './useIntegrationSetupURLs'

export function IntegrationAdvanced({ integration }: { integration: Integration }) {
  const { apiOrigin, unavailable } = useIntegrationSetupURLs()
  const tools = Object.entries(integration.capabilities.tools)
  const providerId = integration.provider_tenant_id ?? ''
  return (
    <Collapsible asChild>
      <section aria-label="Advanced" className="text-sm">
        <h2>
          <CollapsibleTrigger className="text-muted-foreground hover:text-foreground focus-visible:ring-ring group flex items-center gap-1.5 rounded-sm outline-none focus-visible:ring-2">
            <ChevronRightIcon className="size-4 shrink-0 transition-transform group-data-[state=open]:rotate-90" />
            Advanced
          </CollapsibleTrigger>
        </h2>
        <CollapsibleContent className="pl-5.5 flex flex-col gap-6 pt-4">
          <div className="flex flex-col gap-2">
            <h3 className="font-medium">Connection details</h3>
            {providerId ? (
              <p className="text-muted-foreground break-words">
                {integration.integration_kind === 'slack_thread' ? 'Workspace' : 'Application'}{' '}
                {providerId} · {integration.provider_account_ref}
              </p>
            ) : (
              <p className="text-muted-foreground">No account has been connected.</p>
            )}
            {integration.integration_kind === 'github_pr' && (
              <p className="text-muted-foreground">
                Webhook URL:{' '}
                <code className="text-foreground break-all">
                  {apiOrigin
                    ? `${apiOrigin}/api/integrations/github/events`
                    : unavailable
                      ? 'Public API URL unavailable'
                      : 'Loading setup URL…'}
                </code>
                . Subscribe to pull requests, issue comments, pull request reviews, and pull request
                review comments.
              </p>
            )}
            {integration.integration_kind === 'discord_thread' && (
              <p className="text-muted-foreground">
                Interactions Endpoint URL:{' '}
                <code className="text-foreground break-all">
                  {apiOrigin
                    ? `${apiOrigin}/api/integrations/discord/${providerId || 'APPLICATION_ID'}/interactions`
                    : unavailable
                      ? 'Public API URL unavailable'
                      : 'Loading setup URL…'}
                </code>
              </p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <h3 className="font-medium">Agent configurations</h3>
            <p className="text-muted-foreground">
              Nothing here needs to be set up by hand. Agents started from the agent profiles this
              integration launches get these tools and its interaction handler automatically. The
              profile itself is not changed, and entries it already defines, including disabled
              ones, are kept as they are. The names below are a reference for agent configurations
              and the API. The integration name{' '}
              <code className="text-foreground">{integration.name}</code> is permanent, and each
              capability uses this integration’s account and credentials in the launched
              conversation.
            </p>
            <ul className="flex flex-col gap-2">
              {tools.map(([operation, capability]) => (
                <li key={operation}>
                  <code className="break-all">
                    int__{integration.name}__{operation}
                  </code>
                  {capability.description && (
                    <p className="text-muted-foreground">{capability.description}</p>
                  )}
                </li>
              ))}
            </ul>
            {integration.capabilities.subscription && (
              <>
                <h4 className="pt-2 font-medium">Conversation subscriptions</h4>
                <p className="text-muted-foreground">
                  Connect a conversation to an agent through this integration’s subscriptions API.
                  The integration determines which activity is forwarded.
                </p>
              </>
            )}
            {integration.capabilities.interaction_handler && (
              <>
                <h4 className="pt-2 font-medium">Interaction handler</h4>
                <p className="text-muted-foreground">
                  Listed under <code>interaction_handlers</code> for questions and approvals in the
                  launched conversation: <code className="text-foreground">{integration.name}</code>
                </p>
              </>
            )}
          </div>
        </CollapsibleContent>
      </section>
    </Collapsible>
  )
}
