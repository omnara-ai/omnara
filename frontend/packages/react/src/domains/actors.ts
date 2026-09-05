import { listActorsOptions } from '@omnara/sdk/tanstack'
import { type Query, useSuspenseQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { generatedQueryKey } from './query-keys'

export function useCurrentActorId(
  orgID: string,
  projectID: string,
  currentUserId: string,
): string | undefined {
  const client = useOmnaraClient()
  const query = useSuspenseQuery(
    listActorsOptions({
      path: { orgID, projectID },
      query: { provider: 'omnara', provider_user_id: currentUserId, limit: 1 },
      client,
    }),
  )
  return query.data.data[0]?.id
}

export function projectActorsQueryPredicate(orgID: string, projectID: string) {
  return (query: Query): boolean => {
    const key = generatedQueryKey(query)
    return (
      key?._id === 'listActors' && key.path?.orgID === orgID && key.path.projectID === projectID
    )
  }
}
