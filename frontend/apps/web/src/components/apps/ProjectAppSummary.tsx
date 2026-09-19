import { useAgentProfileQuery, useIntegrationConnection } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

export function ProjectAppSummary({
  orgId,
  projectId,
  app,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
}) {
  const { resource, launcher } = app.settings
  return (
    <div className="flex flex-col gap-6 text-sm">
      <AppConnectionSummary
        orgId={orgId}
        projectId={projectId}
        connectionId={resource.connection}
      />
      <AppLauncherSummary orgId={orgId} projectId={projectId} launcher={launcher} />
      <section className="flex flex-col gap-2" aria-label="Conversation scope">
        <h2 className="font-medium">Conversation scope</h2>
        <p>{scopeDescription(resource.scope)}</p>
        {resource.enabled === false && (
          <Badge className="self-start" variant="secondary">
            App capabilities disabled
          </Badge>
        )}
      </section>
      <AppCapabilitiesSummary resource={resource} />
    </div>
  )
}

function AppConnectionSummary({
  orgId,
  projectId,
  connectionId,
}: {
  orgId: string
  projectId: string
  connectionId?: string
}) {
  const connection = useIntegrationConnection(orgId, projectId, connectionId ?? '')
  return (
    <section className="flex flex-col gap-2" aria-label="Connection">
      <h2 className="font-medium">Connection</h2>
      {connectionId ? (
        connection.isPending ? (
          <p>Loading connection…</p>
        ) : connection.isError ? (
          <div role="alert" className="flex items-center gap-2">
            Connection unavailable.
            <Button size="sm" variant="outline" onClick={() => void connection.refetch()}>
              Retry
            </Button>
          </div>
        ) : (
          <p className="flex flex-wrap items-center gap-2">
            {connection.data.provider_agent_display_name || connection.data.id}
            <Badge variant={connection.data.state === 'active' ? 'outline' : 'secondary'}>
              {connection.data.state}
            </Badge>
          </p>
        )
      ) : (
        <p className="text-muted-foreground">No provider connection.</p>
      )}
      <Link
        className="text-muted-foreground underline underline-offset-2"
        to="/projects/$projectId/apps"
        params={{ projectId }}
        hash="connections"
      >
        Manage project connections
      </Link>
    </section>
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
          No automatic launches. Select this app’s capabilities in an agent configuration.
        </p>
      )}
    </section>
  )
}

function AppCapabilitiesSummary({ resource }: { resource: ProjectApp['settings']['resource'] }) {
  const tools = Object.entries(resource.tools ?? {})
    .filter(([, tool]) => tool.enabled !== false)
    .map(([name]) => name)
  return (
    <section className="flex flex-col gap-2" aria-label="Capabilities">
      <h2 className="font-medium">Capabilities</h2>
      <p>Tools: {tools.length ? tools.join(', ') : 'None'}</p>
      {resource.mcp && Object.keys(resource.mcp).length > 0 && (
        <p>MCP servers: {Object.keys(resource.mcp).join(', ')}</p>
      )}
      <p>Incoming events: {resource.listener?.events.join(', ') ?? 'None'}</p>
      <p>
        Follow replies: {resource.follow?.replies ? 'Allowed when requested by the agent' : 'Off'}
      </p>
      <p>
        Questions and approvals:{' '}
        {resource.interaction_handler
          ? 'Available through this app and in the dashboard'
          : 'Dashboard or another configured destination'}
      </p>
      <p className="text-muted-foreground">
        Agent configurations choose which capabilities to use and where they apply. Changes to this
        app apply to future configurations and launches.
      </p>
    </section>
  )
}

function scopeDescription(scope: ProjectApp['settings']['resource']['scope']) {
  if (scope?.slack) {
    return `Slack channel ${scope.slack.channel_id}${scope.slack.thread_ts ? ` · thread ${scope.slack.thread_ts}` : ' and its threads'}`
  }
  if (scope?.github) {
    return `GitHub repository ${scope.github.repository_id} · PR #${scope.github.pull_request}`
  }
  if (scope?.discord) {
    return `Discord channel ${scope.discord.channel_id}${scope.discord.thread_id ? ` · thread ${scope.discord.thread_id}` : ' and its threads'}`
  }
  return 'Provided by the launcher or selected when adding this app to an agent configuration.'
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
