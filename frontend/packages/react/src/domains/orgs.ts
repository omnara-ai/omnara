import {
  type CreateOrganizationRequest,
  type ListOrgInvitationsData,
  type ListOrgMembersData,
  sdk,
} from '@omnara/sdk'
import {
  getCurrentUserQueryKey,
  getOrgOverviewOptions,
  listOrgInvitationsInfiniteOptions,
  listOrgInvitationsQueryKey,
  listOrgMembersInfiniteOptions,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPaginated } from './pagination'
import { useScopedMutation } from './scoped-mutation'

// createOrganization has no path params, so useScopedMutation doesn't apply.
export function useCreateOrganization() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      body,
      idempotencyKey,
    }: {
      body: CreateOrganizationRequest
      idempotencyKey: string
    }) => {
      const { data } = await sdk.createOrganization({
        body,
        headers: { 'Idempotency-Key': idempotencyKey },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: getCurrentUserQueryKey({ client }) })
    },
  })
}

export function useOrgOverview(
  orgID: string,
  options?: Pick<ReturnType<typeof getOrgOverviewOptions>, 'refetchInterval'>,
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getOrgOverviewOptions({ path: { orgID }, client }),
    refetchInterval: options?.refetchInterval,
  })
}

export function useInviteMember(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createOrgInvitation,
    { orgID },
    {
      onSuccess: async () => {
        await queryClient.invalidateQueries({
          queryKey: listOrgInvitationsQueryKey({ path: { orgID }, client }),
        })
      },
    },
  )
}

export type OrgInvitationListFilters = ListFilters<ListOrgInvitationsData>
export type OrgInvitationListOptions = PaginatedListOptions<ListOrgInvitationsData>

export function useOrgInvitations(orgID: string, options?: OrgInvitationListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListOrgInvitationsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listOrgInvitationsInfiniteOptions({
        path: { orgID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useDeleteOrgInvitation(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (invitationID: string) => {
      const { data } = await sdk.deleteOrgInvitation({ path: { orgID, invitationID }, client })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listOrgInvitationsQueryKey({ path: { orgID }, client }),
      })
    },
  })
}

export type OrgMemberListFilters = ListFilters<ListOrgMembersData>
export type OrgMemberListSort = ListSort<ListOrgMembersData>
export type OrgMemberListOptions = PaginatedListOptions<ListOrgMembersData>

export function useOrgMembers(orgID: string, options?: OrgMemberListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListOrgMembersData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listOrgMembersInfiniteOptions({
        path: { orgID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}
