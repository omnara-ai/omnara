import type { Query, QueryClient, QueryKey } from '@tanstack/react-query'
import { z } from 'zod'

const generatedQueryKeyEntry = z.object({
  _id: z.string(),
  path: z
    .object({
      orgID: z.string().optional(),
      projectID: z.string().optional(),
      memoryStoreID: z.string().optional(),
    })
    .optional(),
})

export type GeneratedQueryKeyEntry = z.infer<typeof generatedQueryKeyEntry>

export function generatedQueryKey(query: Query): GeneratedQueryKeyEntry | undefined {
  const parsed = generatedQueryKeyEntry.safeParse(query.queryKey[0])
  return parsed.success ? parsed.data : undefined
}

export function removeQueryWhenInactive(queryClient: QueryClient, queryKey: QueryKey) {
  const cache = queryClient.getQueryCache()
  const query = cache.find({ queryKey, exact: true })
  if (!query) return
  if (query.getObserversCount() === 0) {
    cache.remove(query)
    return
  }
  const unsubscribe = cache.subscribe((event) => {
    if (event.query !== query) return
    if (event.type === 'removed') {
      unsubscribe()
      return
    }
    if (event.type === 'observerRemoved' && query.getObserversCount() === 0) {
      unsubscribe()
      cache.remove(query)
    }
  })
}
