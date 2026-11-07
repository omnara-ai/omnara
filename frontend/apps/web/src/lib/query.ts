import { QueryClient } from '@tanstack/react-query'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      // The SDK owns short transport retries, including browser bootstrap GETs.
      // Query refetch-on-reconnect remains responsible for coming back online.
      retry: false,
    },
  },
})
