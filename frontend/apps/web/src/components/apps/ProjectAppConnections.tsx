import {
  useDeleteIntegrationConnection,
  useIntegrationConnections,
  useOmnaraClient,
} from '@omnara/react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { errorMessage } from '@/lib/submit-status'

import { ConnectSlackDialog } from './ConnectSlackDialog'

export function ProjectAppConnections({
  orgId,
  projectId,
  canManage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
}) {
  const query = useIntegrationConnections(orgId, projectId)
  const connections = useInfiniteQueryItems(query)
  const disconnect = useDeleteIntegrationConnection(orgId, projectId)
  const [slackSetup, setSlackSetup] = useState<'connect' | 'reconnect'>()
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  return (
    <section className="flex flex-col gap-2" aria-label="Project connections">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="font-medium">Project connections</h2>
        {canManage && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setSlackSetup('connect')
            }}
          >
            Connect Slack
          </Button>
        )}
      </div>
      <p className="text-muted-foreground text-sm">
        Provider access shared by all apps and agents in this project.
      </p>
      {query.isPending && <p className="text-sm">Loading connections…</p>}
      {query.isError && (
        <p role="alert">
          Could not load connections.{' '}
          <button type="button" onClick={() => void query.refetch()}>
            Retry
          </button>
        </p>
      )}
      {query.isSuccess && connections.length === 0 && (
        <p className="text-muted-foreground text-sm">No provider connections yet.</p>
      )}
      <ul className="divide-y rounded-md border">
        {connections.map((connection) => (
          <li key={connection.id} className="flex flex-col gap-2 p-3 text-sm">
            <div className="flex items-center justify-between gap-3">
              <span>
                {connection.provider_agent_display_name || connection.provider} · {connection.state}
              </span>
              {canManage && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={disconnect.isPending}
                  onClick={() => {
                    if (
                      !window.confirm(
                        `Disconnect ${connection.provider_agent_display_name || connection.id}? All apps and agents using this connection will lose provider access. Saved apps and credentials remain.`,
                      )
                    )
                      return
                    disconnect.mutate(connection.id, {
                      onError: (error) => {
                        window.alert(errorMessage(error, 'Could not disconnect provider'))
                      },
                    })
                  }}
                >
                  Disconnect
                </Button>
              )}
            </div>
            <p className="text-muted-foreground break-all">
              {connection.provider} · {connection.provider_tenant_id} /{' '}
              {connection.provider_account_ref} · {connection.id}
            </p>
            {canManage && connection.provider === 'slack' && (
              <Button
                className="self-start"
                size="sm"
                variant="outline"
                onClick={() => {
                  setSlackSetup('reconnect')
                }}
              >
                Reauthorize Slack
              </Button>
            )}
            {connection.provider === 'github' && (
              <p className="break-all">
                GitHub App webhook URL:{' '}
                <code>
                  {apiOrigin}/api/integrations/github/{connection.provider_tenant_id}/events
                </code>
                . Subscribe to pull requests, issue comments, and pull request review comments.
              </p>
            )}
            {connection.provider === 'discord' && (
              <p className="text-muted-foreground">
                Application ID {connection.provider_tenant_id}; bot User ID{' '}
                {connection.provider_account_ref}. The bot must be in your server with access to the
                selected channel. For questions and approvals, configure its public key and set the
                Interactions Endpoint URL to{' '}
                <code className="break-all">
                  {apiOrigin}/api/integrations/discord/{connection.id}/interactions
                </code>
                .
              </p>
            )}
          </li>
        ))}
      </ul>
      {query.hasNextPage && (
        <Button
          size="sm"
          variant="outline"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          More connections
        </Button>
      )}
      {slackSetup && (
        <ConnectSlackDialog
          open
          orgId={orgId}
          projectId={projectId}
          reconnect={slackSetup === 'reconnect'}
          onOpenChange={(open) => {
            if (!open) setSlackSetup(undefined)
          }}
        />
      )}
    </section>
  )
}
