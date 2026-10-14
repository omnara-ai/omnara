import { sdk } from '@omnara/sdk'
import { getCurrentUserOptions } from '@omnara/sdk/tanstack'
import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

export function useMe() {
  const client = useOmnaraClient()
  return useSuspenseQuery(getCurrentUserOptions({ client }))
}

// Deleting the account revokes the browser session, so callers leave the app
// on success. Cached queries are only marked stale: refetching would 401.
export function useDeleteCurrentUser() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      await sdk.deleteCurrentUser({ client })
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ refetchType: 'none' })
    },
  })
}
