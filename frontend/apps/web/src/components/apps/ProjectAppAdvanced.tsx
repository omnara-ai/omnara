import { useOmnaraClient } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'

import { ChevronRightIcon } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

/** Reference material for provider portals and agent configurations; closed by default. */
export function ProjectAppAdvanced({ app }: { app: ProjectApp }) {
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  const tools = Object.entries(app.capabilities.tools)
  const subscriptions = Object.entries(app.capabilities.subscriptions)
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
            {app.provider_tenant_id ? (
              <p className="text-muted-foreground break-words">
                {app.app_type === 'slack_thread' ? 'Workspace' : 'Application'}{' '}
                {app.provider_tenant_id} · {app.provider_account_ref}
              </p>
            ) : (
              <p className="text-muted-foreground">No account has been connected.</p>
            )}
            {app.app_type === 'github_pr' && (
              <p className="text-muted-foreground">
                Webhook URL:{' '}
                <code className="text-foreground break-all">
                  {apiOrigin}/api/integrations/github/{app.provider_tenant_id || 'APP_ID'}/events
                </code>
                . Subscribe to pull requests, issue comments, and pull request review comments.
              </p>
            )}
            {app.app_type === 'discord_thread' && (
              <p className="text-muted-foreground">
                Interactions Endpoint URL:{' '}
                <code className="text-foreground break-all">
                  {apiOrigin}/api/integrations/discord/{app.provider_tenant_id || 'APPLICATION_ID'}
                  /interactions
                </code>
              </p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <h3 className="font-medium">Agent configurations</h3>
            <p className="text-muted-foreground">
              Nothing here needs to be set up by hand. Agents started from the agent profiles this
              app launches get these tools and its interaction handler automatically. The profile
              itself is not changed, and entries it already defines, including disabled ones, are
              kept as they are. The names below are a reference for agent configurations and the
              API. The app name <code className="text-foreground">{app.name}</code> is permanent,
              and each capability uses this app’s account and credentials in the launched
              conversation.
            </p>
            <ul className="flex flex-col gap-2">
              {tools.map(([operation, capability]) => (
                <li key={operation}>
                  <code className="break-all">
                    app__{app.name}__{operation}
                  </code>
                  {capability.description && (
                    <p className="text-muted-foreground">{capability.description}</p>
                  )}
                </li>
              ))}
            </ul>
            {subscriptions.length > 0 && (
              <>
                <h4 className="pt-2 font-medium">Subscription types</h4>
                <p className="text-muted-foreground">
                  Use these types with this app’s subscriptions API:
                </p>
                <ul className="flex flex-col gap-2">
                  {subscriptions.map(([name, capability]) => (
                    <li key={name}>
                      <code>{name}</code>
                      <p className="text-muted-foreground">
                        Events: {capability.events.join(', ')}
                      </p>
                    </li>
                  ))}
                </ul>
              </>
            )}
            {app.capabilities.interaction_handler && (
              <>
                <h4 className="pt-2 font-medium">Interaction handler</h4>
                <p className="text-muted-foreground">
                  Listed under <code>interaction_handlers</code> for questions and approvals in the
                  launched conversation: <code className="text-foreground">{app.name}</code>
                </p>
              </>
            )}
          </div>
        </CollapsibleContent>
      </section>
    </Collapsible>
  )
}
