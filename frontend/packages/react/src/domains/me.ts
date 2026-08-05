import { getCurrentUserOptions } from '@omnara/sdk/tanstack'
import { useSuspenseQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

export function useMe() {
  const client = useOmnaraClient()
  return useSuspenseQuery(getCurrentUserOptions({ client }))
}
