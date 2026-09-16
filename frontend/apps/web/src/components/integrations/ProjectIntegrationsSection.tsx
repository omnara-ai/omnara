import {
  useDeleteIntegrationInstall,
  useEligibleIntegrationApps,
  useIntegrationInstalls,
} from '@omnara/react'
import type { IntegrationAppSummary, IntegrationInstall } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { integrationProviderLabel } from '@/components/org/integrationCredentials'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { errorMessage } from '@/lib/submit-status'

import { ConnectIntegrationAppDialog } from './ConnectIntegrationAppDialog'
import { IntegrationLaunchProfileDialog } from './IntegrationLaunchProfileDialog'
import { RegisteredChannelsDialog } from './RegisteredChannelsDialog'

export function ProjectIntegrationsSection({
  orgId,
  projectId,
  canManage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
}) {
  const apps = useEligibleIntegrationApps(orgId, projectId)
  const pagedApps = usePagedQuery(apps, projectId)
  const connections = useIntegrationInstalls(orgId, projectId)
  const pagedConnections = usePagedQuery(connections, projectId)
  const remove = useDeleteIntegrationInstall(orgId, projectId)
  const [selected, setSelected] = useState<IntegrationInstall | null>(null)
  const [error, setError] = useState('')
  const [connectingApp, setConnectingApp] = useState<IntegrationAppSummary | null>(null)
  const [profileConnection, setProfileConnection] = useState<IntegrationInstall | null>(null)
  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader title="Connections" />
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (install) =>
                install.display_name || (install.provider_account_ref ?? 'External connection'),
            },
            {
              id: 'provider',
              header: 'Provider',
              cell: (install) =>
                install.provider ? integrationProviderLabel(install.provider) : 'External',
            },
            {
              id: 'status',
              header: 'Status',
              cell: (install) => (install.state === 'active' ? 'Active' : 'Disabled'),
            },
            {
              id: 'actions',
              header: '',
              isActions: true,
              cell: (install) => (
                <div className="flex justify-end gap-2">
                  {install.integration_kind === 'managed' &&
                    ['github', 'discord', 'slack'].includes(install.provider ?? '') && (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => {
                          setProfileConnection(install)
                        }}
                      >
                        Inbound profile
                      </Button>
                    )}
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setSelected(install)
                    }}
                  >
                    Channels
                  </Button>
                  {canManage && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={remove.isPending}
                      onClick={() => {
                        if (
                          !window.confirm(
                            `Disconnect ${install.display_name || (install.provider_account_ref ?? 'this connection')}? Agents are kept, but the connected app stops working.`,
                          )
                        )
                          return
                        setError('')
                        remove.mutate(install.id, {
                          onError: (err) => {
                            setError(errorMessage(err, 'Could not disconnect app'))
                          },
                        })
                      }}
                    >
                      Disconnect
                    </Button>
                  )}
                </div>
              ),
            },
          ]}
          data={pagedConnections.rows}
          pagination={pagedConnections.pagination}
          getRowId={(install) => install.id}
          isPending={connections.isPending}
          isError={connections.isError}
          onRetry={() => {
            void connections.refetch()
          }}
          emptyMessage="No connections yet."
        />
      </div>
      <div className="flex flex-col gap-3">
        <SearchHeader title="Available apps" />
        <p className="text-muted-foreground text-sm">
          Apps available for this project. Organization administrators manage app configuration and
          credentials.
        </p>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (app) => app.name || integrationProviderLabel(app.provider),
            },
            {
              id: 'provider',
              header: 'Provider',
              cell: (app) => integrationProviderLabel(app.provider),
            },
            {
              id: 'availability',
              header: 'Available to',
              cell: (app) => (app.owner_project_id ? 'This project' : 'All projects'),
            },
            {
              id: 'actions',
              header: '',
              isActions: true,
              cell: (app) =>
                canManage && app.state === 'active' ? (
                  <Button
                    size="sm"
                    variant="outline"
                    aria-label={`Connect ${app.name || integrationProviderLabel(app.provider)}`}
                    onClick={() => {
                      setConnectingApp(app)
                    }}
                  >
                    Connect
                  </Button>
                ) : null,
            },
          ]}
          data={pagedApps.rows}
          pagination={pagedApps.pagination}
          getRowId={(app) => app.id}
          isPending={apps.isPending}
          isError={apps.isError}
          onRetry={() => {
            void apps.refetch()
          }}
          emptyMessage="No apps available. Ask an organization administrator to add an app."
        />
      </div>
      {canManage && connectingApp && (
        <ConnectIntegrationAppDialog
          orgId={orgId}
          projectId={projectId}
          app={connectingApp}
          onClose={() => {
            setConnectingApp(null)
          }}
        />
      )}
      {selected && (
        <RegisteredChannelsDialog
          orgId={orgId}
          projectId={projectId}
          install={selected}
          canManage={canManage}
          onClose={() => {
            setSelected(null)
          }}
        />
      )}
      {profileConnection && (
        <IntegrationLaunchProfileDialog
          key={profileConnection.id}
          orgId={orgId}
          projectId={projectId}
          install={profileConnection}
          canManage={canManage}
          onClose={() => {
            setProfileConnection(null)
          }}
        />
      )}
    </>
  )
}
