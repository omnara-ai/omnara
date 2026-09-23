import type { ProjectApp } from '@omnara/sdk'

import { CircleCheck } from '@/components/icons'
import { Button } from '@/components/ui/button'

import { ProjectAppHeader } from './ProjectAppHeader'
import type { useProjectAppActions } from './useProjectAppActions'
import type { SlackOAuthOutcome } from './useSlackOAuthOutcome'

export function ProjectAppConnectionStatus({
  app,
  actions,
  canManage,
  canSetUp,
  draft,
  connecting,
  justConnected,
  onReconnect,
  refreshFailed,
  onRefresh,
  oauth,
}: {
  app: ProjectApp
  actions: ReturnType<typeof useProjectAppActions>
  canManage: boolean
  canSetUp: boolean
  draft: boolean
  connecting: boolean
  justConnected: boolean
  onReconnect: () => void
  refreshFailed: boolean
  onRefresh: () => void
  oauth: SlackOAuthOutcome | null
}) {
  const reconnect = canSetUp && !connecting ? onReconnect : undefined
  return (
    <div className="flex flex-col gap-4">
      <ProjectAppHeader
        actions={canManage ? actions : undefined}
        app={app}
        onReconnect={app.state === 'active' ? reconnect : undefined}
      />
      {refreshFailed && (
        <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
          Could not refresh this app. Your current edits are kept.
          <Button size="sm" variant="outline" onClick={onRefresh}>
            Retry refresh
          </Button>
        </div>
      )}
      {oauth?.kind === 'error' && (
        <p role="alert" className="text-destructive text-sm">
          Slack setup didn’t finish. {oauth.description}
        </p>
      )}
      {justConnected && (
        <ConnectedNotice app={app} chooseNext={canSetUp && !app.settings.launcher} />
      )}
      {app.state === 'disconnected' && (
        <DisconnectedNotice app={app} draft={draft} canSetUp={canSetUp} onReconnect={reconnect} />
      )}
    </div>
  )
}

function DisconnectedNotice({
  app,
  draft,
  canSetUp,
  onReconnect,
}: {
  app: ProjectApp
  draft: boolean
  canSetUp: boolean
  onReconnect?: () => void
}) {
  if (draft)
    return canSetUp ? null : (
      <p className="text-muted-foreground text-sm">
        Setup isn’t finished. Ask a project administrator to connect this app.
      </p>
    )
  const chat = app.app_type === 'slack_thread' || app.app_type === 'discord_thread'
  return (
    <div className="flex flex-col items-start gap-3 text-sm">
      <p className="text-muted-foreground">
        This app is disconnected. {chat ? 'Mentions, schedules' : 'Launches'} and conversation
        forwarding are paused; settings, agents and history are kept.
      </p>
      {onReconnect && <Button onClick={onReconnect}>Reconnect account</Button>}
    </div>
  )
}

function ConnectedNotice({ app, chooseNext }: { app: ProjectApp; chooseNext: boolean }) {
  const chat = app.app_type !== 'github_pr'
  return (
    <p role="status" className="flex items-start gap-2 text-sm">
      <CircleCheck className="text-primary mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>
        Account connected.
        {app.app_type === 'discord_thread' && ' Discord setup steps are below if you need them.'}
        {chooseNext &&
          (chat
            ? ' Choose which agents people can start by mentioning the bot, or add a schedule instead.'
            : ' Choose an agent profile and when pull requests start agents.')}
      </span>
    </p>
  )
}
