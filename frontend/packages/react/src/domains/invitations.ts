import { sdk } from '@omnara/sdk'
import {
  getCurrentUserQueryKey,
  listPendingInvitationsOptions,
  listPendingInvitationsQueryKey,
} from '@omnara/sdk/tanstack'
import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

export function usePendingInvitations() {
  const client = useOmnaraClient()
  return useSuspenseQuery(listPendingInvitationsOptions({ client }))
}

export function useAcceptInvitation() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (invitationID: string) => {
      const { data } = await sdk.acceptInvitation({ path: { invitationID }, client })
      return data
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: listPendingInvitationsQueryKey({ client }) }),
        queryClient.invalidateQueries({ queryKey: getCurrentUserQueryKey({ client }) }),
      ])
    },
  })
}

export function useDeclineInvitation() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (invitationID: string) => {
      const { data } = await sdk.declineInvitation({ path: { invitationID }, client })
      return data
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: listPendingInvitationsQueryKey({ client }) })
    },
  })
}
