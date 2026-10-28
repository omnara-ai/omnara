import { sdk } from '@omnara/sdk'
import {
  getCurrentUserQueryKey,
  listMemberProjectAccessOptions,
  listMemberProjectAccessQueryKey,
  listOrgMembersQueryKey,
} from '@omnara/sdk/tanstack'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

export function useUpdateOrgMemberRole(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ userID, role }: { userID: string; role: string }) => {
      const { data } = await sdk.updateOrgMember({
        path: { orgID, userID },
        body: { role },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listOrgMembersQueryKey({ path: { orgID }, client }),
        }),
        queryClient.invalidateQueries({ queryKey: getCurrentUserQueryKey({ client }) }),
      ])
    },
  })
}

export function useRemoveOrgMember(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (userID: string) => {
      const { data } = await sdk.removeOrgMember({ path: { orgID, userID }, client })
      return data
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: listOrgMembersQueryKey({ path: { orgID }, client }),
        }),
        queryClient.invalidateQueries({ queryKey: getCurrentUserQueryKey({ client }) }),
      ])
    },
  })
}

export function useMemberProjectAccess(
  orgID: string,
  userID: string,
  options?: { enabled?: boolean },
) {
  const client = useOmnaraClient()
  return useQuery({
    ...listMemberProjectAccessOptions({ path: { orgID, userID }, client }),
    enabled: (options?.enabled ?? true) && userID !== '',
  })
}

export function useSetMemberProjectAccess(orgID: string, userID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ projectID, role }: { projectID: string; role: string }) => {
      const { data } = await sdk.setMemberProjectAccess({
        path: { orgID, userID, projectID },
        body: { role },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listMemberProjectAccessQueryKey({ path: { orgID, userID }, client }),
      })
    },
  })
}

export function useRemoveMemberProjectAccess(orgID: string, userID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (projectID: string) => {
      const { data } = await sdk.removeMemberProjectAccess({
        path: { orgID, userID, projectID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: listMemberProjectAccessQueryKey({ path: { orgID, userID }, client }),
      })
    },
  })
}
