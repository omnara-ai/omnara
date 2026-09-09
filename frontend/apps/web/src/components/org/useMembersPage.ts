import { useOrgInvitations, useOrgMembers } from '@omnara/react'
import type { OrgInvitation, OrgMember } from '@omnara/sdk'
import { useState } from 'react'

import type { PaginationControls } from '@/hooks/use-paged-query'

type MembershipRow =
  | { kind: 'invitation'; id: string; invitation: OrgInvitation }
  | { kind: 'member'; id: string; member: OrgMember }

const PAGE_SIZE = 15

export function useMembersPage(orgId: string, canManage: boolean) {
  const [page, setPage] = useState(0)
  const [paginationOwner, setPaginationOwner] = useState({
    orgID: orgId,
    canManage,
  })
  if (paginationOwner.orgID !== orgId || paginationOwner.canManage !== canManage) {
    setPaginationOwner({ orgID: orgId, canManage })
    setPage(0)
  }

  const invitationsQuery = useOrgInvitations(orgId, {
    pageSize: PAGE_SIZE,
    enabled: canManage,
  })
  const invitationsExhausted =
    !canManage ||
    invitationsQuery.isError ||
    (invitationsQuery.isSuccess && !invitationsQuery.hasNextPage)
  const membersQuery = useOrgMembers(orgId, {
    sort: 'name',
    pageSize: PAGE_SIZE,
    enabled: invitationsExhausted,
  })

  const loadedRows: MembershipRow[] = []
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

  return { invitationsQuery, membersQuery, invitationsExhausted, rows, pagination, setPage }
}
