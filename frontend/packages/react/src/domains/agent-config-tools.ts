import {
  type ResolveAgentConfigToolsRequest,
  type ResolvedAgentConfigTools,
  sdk,
} from '@omnara/sdk'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useId } from 'react'

import { useOmnaraClient } from '../omnara-client'

export function useAgentConfigTools(
  orgID: string,
  projectID: string,
  request: ResolveAgentConfigToolsRequest,
) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const editorID = useId()
  const scopeKey = ['agent-config-tools', orgID, projectID, editorID] as const
  return useQuery({
    queryKey: [...scopeKey, request],
    queryFn: async ({ signal }) => {
      const { data } = await sdk.resolveAgentConfigTools({
        path: { orgID, projectID },
        body: request,
        client,
        signal,
      })
      queryClient.removeQueries({
        queryKey: scopeKey,
        type: 'inactive',
        predicate: (query) => query.state.dataUpdatedAt === 0,
      })
      return data
    },
    initialData: () => {
      const previous = queryClient
        .getQueryCache()
        .findAll({ queryKey: scopeKey })
        .filter((query) => query.state.data !== undefined)
        .sort(
          (a, b) =>
            Number(b.isActive()) - Number(a.isActive()) ||
            b.state.dataUpdatedAt - a.state.dataUpdatedAt,
        )[0]
      return previous
        ? queryClient.getQueryData<ResolvedAgentConfigTools>(previous.queryKey)
        : undefined
    },
    initialDataUpdatedAt: 0,
    enabled: orgID !== '' && projectID !== '',
    staleTime: 60 * 1000,
    retry: false,
  })
}
