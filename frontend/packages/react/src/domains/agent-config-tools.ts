import { type ResolveAgentConfigToolsRequest, sdk } from '@omnara/sdk'
import { useQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

export function useAgentConfigTools(
  orgID: string,
  projectID: string,
  request: ResolveAgentConfigToolsRequest,
) {
  const client = useOmnaraClient()
  return useQuery({
    queryKey: ['agent-config-tools', orgID, projectID, request],
    queryFn: async ({ signal }) => {
      const { data } = await sdk.resolveAgentConfigTools({
        path: { orgID, projectID },
        body: request,
        client,
        signal,
      })
      return data
    },
    enabled: orgID !== '' && projectID !== '',
    staleTime: 60 * 1000,
    retry: false,
  })
}
