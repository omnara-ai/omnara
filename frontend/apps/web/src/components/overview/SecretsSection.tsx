import { type SecretListSort, type SecretOwnerScope, useSecrets } from '@omnara/react'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateSecretDialog } from '@/components/org/CreateSecretDialog'
import { McpOAuthOutcomeDialog } from '@/components/secrets/McpOAuthOutcomeDialog'
import { SecretRowActions } from '@/components/secrets/SecretRowActions'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { secretSubtitle } from '@/lib/secrets'
import { useActiveOrg } from '@/lib/use-active-org'

export function SecretsSection({
  owner = { kind: 'org' },
  canRead: canReadOverride,
  canManage: canManageOverride,
}: {
  owner?: SecretOwnerScope
  canRead?: boolean
  canManage?: boolean
}) {
  const { activeOrg } = useActiveOrg()
  const canManage =
    canManageOverride ??
    (owner.kind === 'user' || (owner.kind === 'org' && canManageOrg(activeOrg.role)))
  // Secret metadata has a dedicated list permission. The current API exposes
  // it through org management and project secret management, respectively.
  const canRead = canReadOverride ?? canManage

  if (!canRead) {
    return (
      <p className="text-muted-foreground text-sm">
        You don’t have permission to view secrets here.
      </p>
    )
  }

  return <SecretsList owner={owner} canManage={canManage} />
}

function SecretsList({ owner, canManage }: { owner: SecretOwnerScope; canManage: boolean }) {
  const { activeOrg } = useActiveOrg()
  const list = useResourceList<SecretListSort>('-updated_at')
  const query = useSecrets(activeOrg.id, owner, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const [open, setOpen] = useState(false)

  const newSecretButton = () =>
    canManage ? (
      <Button
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
      >
        New secret
      </Button>
    ) : undefined

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Secrets"
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={list.sort}
              sortOptions={resourceSortOptions}
              onSortChange={list.setSort}
              placeholder="Search secrets by name…"
            />
          }
        >
          {newSecretButton()}
        </SearchHeader>
        <DataTable
          columns={[
            { header: 'Name' },
            { header: 'Type' },
            { header: '', className: 'w-14', isActions: true },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(secret) => secret.id}
          rowCells={(secret) => [
            <span className="inline-flex max-w-full items-center gap-2">
              <span className="truncate font-medium">{secret.name}</span>
              {secret.management_kind === 'cluster' && <Badge variant="secondary">cluster</Badge>}
            </span>,
            <span className="text-muted-foreground">{secretSubtitle(secret)}</span>,
            <SecretRowActions
              orgId={activeOrg.id}
              secret={secret}
              canDelete={canManage}
              canEdit={canManage}
              canGrant={canManage}
            />,
          ]}
          rowExpanded={(secret) => (
            <DetailList
              items={[
                { label: 'ID', value: secret.id, mono: true },
                {
                  label: 'Managed by',
                  value: secret.management_kind === 'cluster' ? 'Cluster' : 'Organization',
                },
                { label: 'MCP URL', value: secret.metadata.mcp_url, mono: true },
                { label: 'Payload keys', value: secret.payload_keys.join(', '), mono: true },
                { label: 'Version', value: secret.current_version_number },
                { label: 'Created', value: formatDateTime(secret.created_at) },
                { label: 'Updated', value: formatDateTime(secret.updated_at) },
              ]}
            />
          )}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No secrets yet. Add the API keys and credentials your providers and pools use."
        />
      </div>
      {canManage && (
        <CreateSecretDialog open={open} onOpenChange={setOpen} orgId={activeOrg.id} owner={owner} />
      )}
      <McpOAuthOutcomeDialog />
    </>
  )
}
