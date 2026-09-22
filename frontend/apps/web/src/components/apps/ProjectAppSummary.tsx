import { useAgentProfileQuery } from '@omnara/react'
import type { AppLauncher, ProjectApp } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

/** What starts agents for this app, in plain words. */
export function ProjectAppSummary({
  orgId,
  projectId,
  app,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
}) {
  const launcher = app.settings.launcher
  const chat = app.app_type !== 'github_pr'
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
        {launchMoment(launcher, app.app_type)},{' '}
        {chat && launcher.slots.length > 1 ? 'they choose one to start:' : 'Omnara starts:'}
      </p>
      <ul className="flex flex-col gap-1.5">
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
  )
}

function launchMoment(launcher: AppLauncher, appType: ProjectApp['app_type']) {
  if (appType === 'discord_thread')
    return 'When someone mentions the bot in any server where it has access'
  const where =
    launcher.scope_kind === 'workspace'
      ? 'anywhere it has been added in the workspace'
      : `in ${launcher.scope_kind} ${launcher.scope_ref}`
  if (launcher.trigger === 'pull_request_opened') return `When a pull request opens ${where}`
  return launcher.scope_kind === 'repository'
    ? `When someone mentions the bot on a pull request ${where}`
    : `When someone mentions the bot ${where}`
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
