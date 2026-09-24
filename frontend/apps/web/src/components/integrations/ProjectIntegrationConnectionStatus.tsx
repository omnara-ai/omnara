import type { ProjectIntegration } from '@omnara/sdk'

import { CircleCheck } from '@/components/icons'
import { Button } from '@/components/ui/button'

import { ProjectIntegrationHeader } from './ProjectIntegrationHeader'
import type { useProjectIntegrationActions } from './useProjectIntegrationActions'
import type { SlackOAuthOutcome } from './useSlackOAuthOutcome'

export function ProjectIntegrationConnectionStatus({
  integration,
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
  integration: ProjectIntegration
  actions: ReturnType<typeof useProjectIntegrationActions>
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
      <ProjectIntegrationHeader
        actions={canManage ? actions : undefined}
        integration={integration}
        onReconnect={integration.state === 'active' ? reconnect : undefined}
      />
      {refreshFailed && (
        <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
          Could not refresh this integration. Your current edits are kept.
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
      {integration.state === 'active' && integration.runtime_failure && (
        <div role="alert" className="flex flex-col items-start gap-2 text-sm">
          <p>Connection failed: {integration.runtime_failure.message}</p>
          <p>
            Retry available after{' '}
            <time dateTime={integration.runtime_failure.retry_at}>
              {new Date(integration.runtime_failure.retry_at).toLocaleString()}
            </time>
            .
          </p>
          <Button variant="outline" size="sm" onClick={onRefresh}>
            Refresh status
          </Button>
        </div>
      )}
      {justConnected && !integration.runtime_failure && (
        <ConnectedNotice
          integration={integration}
          chooseNext={canSetUp && !integration.settings.launcher}
        />
      )}
      {integration.state === 'disconnected' && (
        <DisconnectedNotice
          integration={integration}
          draft={draft}
          canSetUp={canSetUp}
          onReconnect={reconnect}
        />
      )}
    </div>
  )
}

function DisconnectedNotice({
  integration,
  draft,
  canSetUp,
  onReconnect,
}: {
  integration: ProjectIntegration
  draft: boolean
  canSetUp: boolean
  onReconnect?: () => void
}) {
  if (draft)
    return canSetUp ? null : (
      <p className="text-muted-foreground text-sm">
        Setup isn’t finished. Ask a project administrator to connect this integration.
      </p>
    )
  const chat =
    integration.integration_type === 'slack_thread' ||
    integration.integration_type === 'discord_thread'
  return (
    <div className="flex flex-col items-start gap-3 text-sm">
      <p className="text-muted-foreground">
        This integration is disconnected. {chat ? 'Mentions, schedules' : 'Launches'} and
        conversation forwarding are stopped; settings, agents and history are kept. Events received
        while disconnected are not queued for replay after reconnection.
      </p>
      {onReconnect && <Button onClick={onReconnect}>Reconnect account</Button>}
    </div>
  )
}

function ConnectedNotice({
  integration,
  chooseNext,
}: {
  integration: ProjectIntegration
  chooseNext: boolean
}) {
  const chat = integration.integration_type !== 'github_pr'
  return (
    <p role="status" className="flex items-start gap-2 text-sm">
      <CircleCheck className="text-primary mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>
        Account connected.
        {integration.integration_type === 'discord_thread' &&
          ' Discord setup steps are below if you need them.'}
        {chooseNext &&
          (chat
            ? ' Choose which agents people can start by mentioning the bot, or add a schedule instead.'
            : ' Choose an agent profile and when pull requests start agents.')}
      </span>
    </p>
  )
}
