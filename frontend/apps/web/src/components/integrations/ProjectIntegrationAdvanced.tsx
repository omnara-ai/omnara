import { useOmnaraClient } from '@omnara/react'
import type { ProjectIntegration } from '@omnara/sdk'

import { ChevronRightIcon } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

export function ProjectIntegrationAdvanced({ integration }: { integration: ProjectIntegration }) {
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  const tools = Object.entries(integration.capabilities.tools)
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
            {integration.provider_tenant_id ? (
              <p className="text-muted-foreground break-words">
                {integration.integration_type === 'slack_thread' ? 'Workspace' : 'Application'}{' '}
                {integration.provider_tenant_id} · {integration.provider_account_ref}
              </p>
            ) : (
              <p className="text-muted-foreground">No account has been connected.</p>
            )}
            {integration.integration_type === 'github_pr' && (
              <p className="text-muted-foreground">
                Webhook URL:{' '}
                <code className="text-foreground break-all">
                  {apiOrigin}/api/integrations/github/{integration.provider_tenant_id || 'APP_ID'}
                  /events
                </code>
                . Subscribe to pull requests, issue comments, and pull request review comments.
              </p>
            )}
            {integration.integration_type === 'discord_thread' && (
              <p className="text-muted-foreground">
                Interactions Endpoint URL:{' '}
                <code className="text-foreground break-all">
                  {apiOrigin}/api/integrations/discord/
                  {integration.provider_tenant_id || 'APPLICATION_ID'}
                  /interactions
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
