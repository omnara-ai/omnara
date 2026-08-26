import { type DataTag, hashKey, type QueryKey, useQueryClient } from '@tanstack/react-query'
import { startTransition, useEffect, useState } from 'react'

export function useQuerySnapshotInTransition<TData, TError>(
  queryKey: DataTag<QueryKey, TData, TError>,
): TData | undefined {
  const queryClient = useQueryClient()
  const queryHash = hashKey(queryKey)
  const [snapshot, setSnapshot] = useState(() => ({
    hash: queryHash,
    data: queryClient.getQueryData(queryKey),
  }))

  useEffect(() => {
    const cache = queryClient.getQueryCache()
    return cache.subscribe((event) => {
      if (event.query.queryHash !== queryHash) return
      const data = queryClient.getQueryData(queryKey)
      startTransition(() => {
        setSnapshot({ hash: queryHash, data })
      })
    })
  }, [queryClient, queryHash, queryKey])

  return snapshot.hash === queryHash ? snapshot.data : queryClient.getQueryData(queryKey)
}
