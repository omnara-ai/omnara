import { useAgentProfileQuery } from '@omnara/react'
import {
  chatIntegrationLauncher,
  githubIntegrationSettings,
  type Integration,
  profileIntegrationProfiles,
} from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

export function IntegrationSummary({
  orgId,
  projectId,
  integration,
}: {
  orgId: string
  projectId: string
  integration: Integration
}) {
  const launcher = integration.settings.launcher
  const profiles = profileIntegrationProfiles(integration)
  const chat = integration.integration_kind !== 'github_pr'
  if (!launcher)
    return (
      <p className="text-muted-foreground">
        {chat
          ? 'Mentions don’t start agents yet. Choose the agent profiles people can start, or leave this empty and use schedules only.'
          : 'Pull requests don’t start agents yet. Choose an agent profile to launch for pull request events.'}
      </p>
    )
  return (
    <>
      <p className="text-muted-foreground">
        {launchMoment(integration)},{' '}
        {chat && profiles.length > 1 ? 'they choose one to start:' : 'Omnara starts:'}
      </p>
      <ul className="flex flex-col gap-1.5">
        {profiles.map((profileId) => (
          <li key={profileId}>
            <IntegrationProfileLink orgId={orgId} projectId={projectId} profileId={profileId} />
          </li>
        ))}
      </ul>
    </>
  )
}

function launchMoment(integration: Integration) {
  if (integration.integration_kind === 'discord_thread')
    return 'When someone mentions the bot in any server where it has access'
  if (integration.integration_kind === 'github_pr') {
    const launcher = githubIntegrationSettings(integration.settings).launcher
    const where = launcher?.repository_id
      ? `in repository ${launcher.repository_id}`
      : 'in repositories granted to this installation'
    return launcher?.trigger === 'pull_request_opened'
      ? `When a pull request opens ${where}`
      : `When someone mentions the bot on a pull request ${where}`
  }
  const channel = chatIntegrationLauncher(integration.settings)?.channel_id
  return channel
    ? `When someone mentions the bot in channel ${channel}`
    : 'When someone mentions the bot anywhere it has been added in the workspace'
}

function IntegrationProfileLink({
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
