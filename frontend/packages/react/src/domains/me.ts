import { sdk } from '@omnara/sdk'
import { getCurrentUserOptions } from '@omnara/sdk/tanstack'
import { useMutation, useSuspenseQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

export function useMe() {
  const client = useOmnaraClient()
  return useSuspenseQuery(getCurrentUserOptions({ client }))
}

// Deleting the account revokes the browser session, so callers leave the app
// on success instead of refreshing cached queries.
export function useDeleteCurrentUser() {
  const client = useOmnaraClient()
  return useMutation({
    mutationFn: async () => {
      await sdk.deleteCurrentUser({ client })
    },
  })
}
