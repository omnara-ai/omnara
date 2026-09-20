import { useAgentProfileQuery, useOmnaraClient } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

export function ProjectAppSummary({
  orgId,
  projectId,
  app,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
}) {
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  return (
    <div className="flex flex-col gap-6 text-sm">
      <section className="flex flex-col gap-2" aria-label="Account">
        <h2 className="font-medium">Account</h2>
        <p>
          {app.state === 'active' ? app.provider_agent_display_name || 'Connected' : 'Disconnected'}
        </p>
        {app.provider_tenant_id && (
          <p className="text-muted-foreground">
            {app.provider === 'slack' ? 'Workspace' : 'Application'} {app.provider_tenant_id} ·{' '}
            {app.provider_account_ref}
          </p>
        )}
        {app.provider === 'github' && (
          <p>
            GitHub webhook URL:{' '}
            <code className="break-all">
              {apiOrigin}/api/integrations/github/{app.provider_tenant_id || 'APP_ID'}/events
            </code>
            . Subscribe to pull requests, issue comments, and pull request review comments.
          </p>
        )}
        {app.provider === 'discord' && (
          <p>
            Discord Interactions Endpoint URL:{' '}
            <code className="break-all">
              {apiOrigin}/api/integrations/discord/{app.provider_tenant_id || 'APPLICATION_ID'}
              /interactions
            </code>
            .
          </p>
        )}
      </section>
      <AppLauncherSummary orgId={orgId} projectId={projectId} launcher={app.settings.launcher} />
      <section className="flex flex-col gap-2" aria-label="Capabilities">
        <h2 className="font-medium">Available capabilities</h2>
        <p className="text-muted-foreground">
          Select tools, listeners and interaction handlers separately in an agent configuration.
          Each capability uses this app’s account and credentials.
        </p>
        <ul className="flex flex-col gap-2">
          {Object.entries(app.capabilities.tools).map(([operation, capability]) => (
            <li key={operation}>
              <code>
                app__{app.name}__{operation}
              </code>
              {capability.description && (
                <p className="text-muted-foreground">{capability.description}</p>
              )}
            </li>
          ))}
        </ul>
        {Object.keys(app.capabilities.listeners).length > 0 && (
          <div>
            <h3 className="font-medium">Listeners</h3>
            <p className="text-muted-foreground">
              Select under <code>listeners</code>:
            </p>
            <ul className="flex flex-col gap-2">
              {Object.entries(app.capabilities.listeners).map(([name, capability]) => (
                <li key={name}>
                  <code>
                    {app.name}__{name}
                  </code>
                  {capability.description && (
                    <p className="text-muted-foreground">{capability.description}</p>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )}
        {app.capabilities.interaction_handler && (
          <div>
            <h3 className="font-medium">Interaction handler</h3>
            <p className="text-muted-foreground">
              Select under <code>interaction_handlers</code> for questions and approvals:
            </p>
            <code>{app.name}</code>
          </div>
        )}
      </section>
    </div>
  )
}

function AppLauncherSummary({
  orgId,
  projectId,
  launcher,
}: {
  orgId: string
  projectId: string
  launcher: ProjectApp['settings']['launcher']
}) {
  return (
    <section className="flex flex-col gap-2" aria-label="Launcher">
      <h2 className="font-medium">Launcher</h2>
      {launcher ? (
        <>
          <p>
            {launcher.trigger === 'pull_request_opened'
              ? 'When a pull request opens'
              : 'When someone mentions the bot'}{' '}
            · {launcher.scope_kind} {launcher.scope_ref}
          </p>
          <ul className="flex flex-col gap-1">
            {launcher.slots.map((slot) => (
              <li key={slot.key}>
                {slot.agent_profile_id ? (
                  <AppProfileLink
                    orgId={orgId}
                    projectId={projectId}
                    profileId={slot.agent_profile_id}
                  />
                ) : slot.agent_id ? (
                  <Link
                    className="underline underline-offset-2"
                    to="/projects/$projectId/agents/$agentId/events"
                    params={{ projectId, agentId: slot.agent_id }}
                  >
                    Existing agent {slot.agent_id}
                  </Link>
                ) : (
                  slot.key
                )}
              </li>
            ))}
          </ul>
        </>
      ) : (
        <p className="text-muted-foreground">
          No event launcher configured. Select this app’s capabilities in an agent configuration.
        </p>
      )}
    </section>
  )
}

function AppProfileLink({
  orgId,
  projectId,
  profileId,
}: {
  orgId: string
  projectId: string
  profileId: string
}) {
  const query = useAgentProfileQuery(orgId, projectId, profileId)
  return (
    <Link
      className="underline underline-offset-2"
      to="/projects/$projectId/agent-profiles/$profileId"
      params={{ projectId, profileId }}
    >
      {query.data?.name ?? profileId}
    </Link>
  )
}
