import { usePersonalAccessTokens } from '@omnara/react'
import { useState } from 'react'

import { CreatePersonalAccessTokenDialog } from '@/components/api-tokens/CreatePersonalAccessTokenDialog'
import { PersonalAccessTokenRowActions } from '@/components/api-tokens/PersonalAccessTokenRowActions'
import { DataTable } from '@/components/data-table/DataTable'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { formatDateTime } from '@/lib/format'

export function PersonalAccessTokensSection() {
  const query = usePersonalAccessTokens()
  const paged = usePagedQuery(query)
  const [createOpen, setCreateOpen] = useState(false)

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-2xl font-bold tracking-tight">Personal access tokens</h2>
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
          {
            id: 'name',
            header: 'Name',
            className: 'w-40 md:w-auto',
            cell: (token) => <span className="font-medium">{token.name}</span>,
          },
          {
            id: 'token-id',
            header: 'Token ID',
            className: 'w-44',
            cell: (token) => (
              <span className="text-muted-foreground font-mono text-xs">{token.token_id}</span>
            ),
          },
          {
            id: 'created',
            header: 'Created',
            className: 'w-44',
            cell: (token) => (
              <span className="text-muted-foreground">{formatDateTime(token.created_at)}</span>
            ),
          },
          {
            id: 'last-used',
            header: 'Last used',
            className: 'w-44',
            cell: (token) => (
              <span className="text-muted-foreground">
                {formatDateTime(token.last_used_at) ?? 'Never'}
              </span>
            ),
          },
          {
            id: 'status',
            header: 'Status',
            className: 'w-24',
            cell: (token) =>
              token.revoked_at ? (
                <span>Revoked</span>
              ) : (
                <span className="text-muted-foreground">Active</span>
              ),
          },
          {
            id: 'actions',
            header: '',
            className: 'w-14',
            isActions: true,
            cell: (token) => <PersonalAccessTokenRowActions token={token} />,
          },
        ]}
        data={paged.rows}
        pagination={paged.pagination}
        getRowId={(token) => token.id}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => void query.refetch()}
        emptyMessage="No API tokens yet. Create one to authenticate an API client or command-line tool."
      />
      <CreatePersonalAccessTokenDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  )
}
