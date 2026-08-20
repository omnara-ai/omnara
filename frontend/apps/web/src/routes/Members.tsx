import { useDeleteOrgInvitation, useOrgInvitations, useOrgMembers } from '@omnara/react'
import type { OrgInvitation, OrgMember } from '@omnara/sdk'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { InviteMemberDialog } from '@/components/org/InviteMemberDialog'
import { MemberDetailPanel } from '@/components/org/MemberDetailPanel'
import { Button } from '@/components/ui/button'
import type { PaginationControls } from '@/hooks/use-paged-query'
import { formatDateTime } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'

type CombinedRow =
  | { kind: 'invitation'; id: string; invitation: OrgInvitation }
  | { kind: 'member'; id: string; member: OrgMember }

const PAGE_SIZE = 15

function memberName(member: OrgMember) {
  return member.display_name || member.email || 'Unnamed user'
}

export function Members() {
  const { activeOrg } = useActiveOrg()
  const [inviteOpen, setInviteOpen] = useState(false)
  const canManage = canManageOrg(activeOrg.role)
  const [page, setPage] = useState(0)
  const [paginationOwner, setPaginationOwner] = useState({
    orgID: activeOrg.id,
    canManage,
  })
  if (paginationOwner.orgID !== activeOrg.id || paginationOwner.canManage !== canManage) {
    setPaginationOwner({ orgID: activeOrg.id, canManage })
    setPage(0)
  }

  const invitationsQuery = useOrgInvitations(activeOrg.id, {
    pageSize: PAGE_SIZE,
    enabled: canManage,
  })
  const deleteInvitation = useDeleteOrgInvitation(activeOrg.id)
  const invitationsExhausted =
    !canManage ||
    invitationsQuery.isError ||
    (invitationsQuery.isSuccess && !invitationsQuery.hasNextPage)
  const membersQuery = useOrgMembers(activeOrg.id, {
    sort: 'name',
    pageSize: PAGE_SIZE,
    enabled: invitationsExhausted,
  })

  const loadedRows: CombinedRow[] = []
  for (const loadedPage of invitationsQuery.data?.pages ?? []) {
    for (const invitation of loadedPage.data) {
      loadedRows.push({ kind: 'invitation', id: invitation.id, invitation })
    }
  }
  for (const loadedPage of membersQuery.data?.pages ?? []) {
    for (const member of loadedPage.data) {
      loadedRows.push({ kind: 'member', id: member.user_id, member })
    }
  }
  const pageStart = page * PAGE_SIZE
  const rows = loadedRows.slice(pageStart, pageStart + PAGE_SIZE)
  const nextPageStart = pageStart + PAGE_SIZE
  const nextPageEnd = nextPageStart + PAGE_SIZE
  const hasLoadedNextPage = loadedRows.length > nextPageStart
  const pagination: PaginationControls = {
    page,
    canPrev: page > 0,
    canNext:
      hasLoadedNextPage ||
      invitationsQuery.hasNextPage ||
      (invitationsExhausted && membersQuery.hasNextPage),
    onPrev: () => {
      setPage((current) => Math.max(current - 1, 0))
    },
    onNext: () => {
      if (
        loadedRows.length < nextPageEnd &&
        invitationsQuery.hasNextPage &&
        !invitationsQuery.isFetchingNextPage
      ) {
        void invitationsQuery.fetchNextPage().then(() => {
          setPage((current) => current + 1)
        })
        return
      }
      if (
        loadedRows.length < nextPageEnd &&
        invitationsExhausted &&
        membersQuery.hasNextPage &&
        !membersQuery.isFetchingNextPage
      ) {
        void membersQuery.fetchNextPage().then(() => {
          setPage((current) => current + 1)
        })
        return
      }
      if (hasLoadedNextPage) {
        setPage((current) => current + 1)
      }
    },
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'organization', label: activeOrg.name, to: '/' },
          { id: 'members', label: 'Members' },
        ]}
      />

      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-2xl font-bold tracking-tight">Members</h2>
          {canManage ? (
            <Button
              size="sm"
              onClick={() => {
                setInviteOpen(true)
              }}
            >
              Invite user
            </Button>
          ) : undefined}
        </div>
        {invitationsQuery.isError && (
          <div
            role="alert"
            className="border-destructive/30 bg-destructive/5 flex items-center justify-between gap-3 rounded-md border px-3 py-2"
          >
            <p className="text-destructive text-sm">
              {errorMessage(invitationsQuery.error, 'Could not load pending invitations.')}
            </p>
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="shrink-0"
              disabled={invitationsQuery.isFetching}
              loading={invitationsQuery.isFetching}
              onClick={() => void invitationsQuery.refetch()}
            >
              Retry
            </Button>
          </div>
        )}
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (row) =>
                row.kind === 'invitation' ? (
                  <span className="text-muted-foreground italic">Pending</span>
                ) : (
                  <span className="font-medium">{memberName(row.member)}</span>
                ),
            },
            {
              id: 'email',
              header: 'Email',
              cell: (row) => (
                <span
                  className={row.kind === 'invitation' ? 'font-medium' : 'text-muted-foreground'}
                >
                  {row.kind === 'invitation' ? row.invitation.email : row.member.email || '—'}
                </span>
              ),
            },
            {
              id: 'role',
              header: 'Role',
              className: 'w-28',
              cell: (row) =>
                row.kind === 'invitation' ? (
                  <span className="text-muted-foreground capitalize">
                    {row.invitation.org_role}
                  </span>
                ) : (
                  <span className="capitalize">{row.member.role}</span>
                ),
            },
          ]}
          data={rows}
          pagination={pagination}
          getRowId={(row) => row.id}
          rowExpanded={(row) =>
            row.kind === 'invitation' ? (
              <div className="flex items-end justify-between gap-3 py-1">
                <p className="text-muted-foreground text-sm">
                  Invited {formatDateTime(row.invitation.created_at)}. Waiting for{' '}
                  {row.invitation.email} to accept.
                </p>
                {canManage && (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="text-destructive hover:text-destructive shrink-0"
                    disabled={deleteInvitation.isPending}
                    loading={deleteInvitation.isPending}
                    onClick={() => {
                      if (
                        window.confirm(`Revoke the invitation sent to ${row.invitation.email}?`)
                      ) {
                        deleteInvitation.mutate(row.invitation.id)
                      }
                    }}
                  >
                    Revoke invitation
                  </Button>
                )}
              </div>
            ) : (
              <MemberDetailPanel orgId={activeOrg.id} member={row.member} />
            )
          }
          isPending={
            rows.length === 0 &&
            (invitationsQuery.isPending || (invitationsExhausted && membersQuery.isPending))
          }
          isError={membersQuery.isError}
          onRetry={() => {
            void membersQuery.refetch()
            if (canManage) void invitationsQuery.refetch()
          }}
          emptyMessage="No members in this organization."
        />
      </div>

      {canManage && (
        <InviteMemberDialog
          open={inviteOpen}
          onOpenChange={setInviteOpen}
          onInvited={() => {
            setPage(0)
          }}
          orgId={activeOrg.id}
        />
      )}
    </div>
  )
}
