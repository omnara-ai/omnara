import { useOrgApiKeys } from '@omnara/react'
import { useState } from 'react'

import { CreateOrgApiKeyDialog } from '@/components/api-tokens/CreateOrgApiKeyDialog'
import { OrgApiKeyDetailPanel } from '@/components/api-tokens/OrgApiKeyDetailPanel'
import { DataTable } from '@/components/data-table/DataTable'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { formatDateTime } from '@/lib/format'

export function OrgApiKeysSection({ orgId }: { orgId: string }) {
  const query = useOrgApiKeys(orgId)
  const paged = usePagedQuery(query, orgId)
  const [createOpen, setCreateOpen] = useState(false)

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-2xl font-bold tracking-tight">Organization API tokens</h2>
        <Button
          size="sm"
          onClick={() => {
            setCreateOpen(true)
          }}
        >
          New token
        </Button>
      </div>
      <DataTable
        columns={[
          { header: 'Name' },
          { header: 'Token ID', className: 'w-44' },
          { header: 'Role', className: 'w-24' },
          { header: 'Created', className: 'w-44' },
          { header: 'Last used', className: 'w-44' },
          { header: 'Status', className: 'w-24' },
        ]}
        data={paged.rows}
        pagination={paged.pagination}
        getRowId={(apiKey) => apiKey.id}
        rowCells={(apiKey) => [
          <span className="font-medium">{apiKey.name}</span>,
          <span className="text-muted-foreground font-mono text-xs">{apiKey.token_id}</span>,
          apiKey.org_role ? (
            <Badge variant="outline" className="capitalize">
              {apiKey.org_role}
            </Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
          <span className="text-muted-foreground">{formatDateTime(apiKey.created_at)}</span>,
          <span className="text-muted-foreground">
            {formatDateTime(apiKey.last_used_at) ?? 'Never'}
          </span>,
          apiKey.revoked_at ? (
            <Badge variant="outline">Revoked</Badge>
          ) : (
            <Badge variant="secondary">Active</Badge>
          ),
        ]}
        rowExpanded={(apiKey) => <OrgApiKeyDetailPanel orgId={orgId} apiKey={apiKey} />}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => void query.refetch()}
        emptyMessage="No organization API tokens yet. Create one to give an integration or automation its own org access."
      />
      <CreateOrgApiKeyDialog open={createOpen} onOpenChange={setCreateOpen} orgId={orgId} />
    </div>
  )
}
