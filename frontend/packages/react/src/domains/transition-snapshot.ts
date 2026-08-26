import { type DataTag, hashKey, type QueryKey, useQueryClient } from '@tanstack/react-query'
import { startTransition, useEffect, useState } from 'react'

export function useQuerySnapshotInTransition<TData, TError>(
  queryKey: DataTag<QueryKey, TData, TError>,
): TData | undefined {
  const queryClient = useQueryClient()
  const queryHash = hashKey(queryKey)
  const [data, setData] = useState(() => queryClient.getQueryData(queryKey))

  useEffect(() => {
    const cache = queryClient.getQueryCache()
    return cache.subscribe((event) => {
      if (event.query.queryHash !== queryHash) return
      const next = queryClient.getQueryData(queryKey)
      startTransition(() => {
        setData(next)
      })
    })
  }, [queryClient, queryHash, queryKey])

  return data
}
