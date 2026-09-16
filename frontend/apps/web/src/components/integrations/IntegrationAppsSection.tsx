import { useIntegrationApps } from '@omnara/react'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { integrationProviderLabel } from '@/components/org/integrationCredentials'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { formatDateTime } from '@/lib/format'

import { CreateIntegrationAppDialog, EditIntegrationAppDialog } from './IntegrationAppDialogs'

export function IntegrationAppsSection({
  orgId,
  canManage,
}: {
  orgId: string
  canManage: boolean
}) {
  const query = useIntegrationApps(orgId, { enabled: canManage })
  const paged = usePagedQuery(query, orgId)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  if (!canManage)
    return (
      <p className="text-muted-foreground text-sm">
        Organization administrators manage apps. Available apps can be viewed in each project’s
        Integrations page.
      </p>
    )
  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader title="Apps">
          <Button
            size="sm"
            onClick={() => {
              setCreating(true)
            }}
          >
            New app
          </Button>
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (app) => (
                <span className="font-medium">
                  {app.name || integrationProviderLabel(app.provider)}
                </span>
              ),
            },
            {
              id: 'provider',
              header: 'Provider',
              cell: (app) => integrationProviderLabel(app.provider),
            },
            {
              id: 'scope',
              header: 'Available to',
              cell: (app) => (app.owner_project_id ? 'One project' : 'All projects'),
            },
            {
              id: 'state',
              header: 'Status',
              cell: (app) => (app.state === 'active' ? 'Active' : 'Disabled'),
            },
            {
              id: 'actions',
              header: '',
              isActions: true,
              cell: (app) => (
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={`Edit ${app.name || integrationProviderLabel(app.provider)}`}
                  onClick={() => {
                    setEditing(app.id)
                  }}
                >
                  Edit
                </Button>
              ),
            },
          ]}
          data={paged.rows}
          pagination={paged.pagination}
          getRowId={(app) => app.id}
          rowExpanded={(app) => (
            <DetailList
              items={[
                { label: 'App ID', value: app.provider_app_ref, mono: true },
                {
                  label: 'Project',
                  value: app.owner_project_id ?? 'All projects',
                  mono: Boolean(app.owner_project_id),
                },
                { label: 'Created', value: formatDateTime(app.created_at) },
              ]}
            />
          )}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No apps yet. Add a GitHub, Discord, or Slack app to make it available to projects."
        />
      </div>
      {creating && (
        <CreateIntegrationAppDialog
          orgId={orgId}
          onClose={() => {
            setCreating(false)
          }}
        />
      )}
      {editing && (
        <EditIntegrationAppDialog
          orgId={orgId}
          appId={editing}
          onClose={() => {
            setEditing(null)
          }}
        />
      )}
    </>
  )
}
