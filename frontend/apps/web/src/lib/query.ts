import { ApiError } from '@omnara/sdk'
import { QueryClient } from '@tanstack/react-query'
import { ZodError } from 'zod'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status >= 400 && error.status < 500) {
          return false
        }
        // A response that fails schema validation will keep failing; retrying
        // only hides the contract violation.
        if (error instanceof ZodError) {
          return false
        }
        return failureCount < 2
      },
    },
  },
})
