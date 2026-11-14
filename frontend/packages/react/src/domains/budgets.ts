import { type BudgetScope, sdk, type UpdateBudgetRequest } from '@omnara/sdk'
import { listBudgetsOptions } from '@omnara/sdk/tanstack'
import { type QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { generatedQueryKey } from './query-keys'
import { useScopedMutation } from './scoped-mutation'

export function useBudgets(orgID: string, scope: BudgetScope) {
  const client = useOmnaraClient()
  const { type, ...ids } = scope
  return useQuery(
    listBudgetsOptions({ client, path: { orgID }, query: { scope_type: type, ...ids } }),
  )
}

export function useCreateBudget(orgID: string) {
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createBudget,
    { orgID },
    {
      onSuccess: async () => {
        await invalidateBudgetQueries(queryClient, orgID)
      },
    },
  )
}

export function useUpdateBudget(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ budgetID, ...body }: UpdateBudgetRequest & { budgetID: string }) => {
      const { data } = await sdk.updateBudget({ client, path: { orgID, budgetID }, body })
      return data
    },
    onSuccess: async () => {
      await invalidateBudgetQueries(queryClient, orgID)
    },
  })
}

export function useDeleteBudget(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (budgetID: string) => {
      await sdk.deleteBudget({ client, path: { orgID, budgetID } })
    },
    onSuccess: async () => {
      await invalidateBudgetQueries(queryClient, orgID)
    },
  })
}

export function invalidateBudgetQueries(queryClient: QueryClient, orgID: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = generatedQueryKey(query)
      return entry?._id === 'listBudgets' && entry.path?.orgID === orgID
    },
  })
}
