import { type McpServerToolsRequest, sdk } from '@omnara/sdk'
import { useQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

const mcpServerToolsStaleTime = 60 * 1000

export function useMcpServerTools(
  orgID: string,
  projectID: string,
  request: McpServerToolsRequest | null,
  options?: { onError?: (error: unknown) => void },
) {
  const client = useOmnaraClient()
  return useQuery({
    queryKey: ['mcp-server-tools', orgID, projectID, request],
    queryFn: async ({ signal }) => {
      if (request == null) throw new Error('mcp server tools request is required')
      try {
        const { data } = await sdk.listMcpServerTools({
          path: { orgID, projectID },
          body: request,
          client,
          signal,
        })
        return data
      } catch (error) {
        if (!signal.aborted) options?.onError?.(error)
        throw error
      }
    },
    enabled: request != null && orgID !== '' && projectID !== '',
    staleTime: mcpServerToolsStaleTime,
    retry: false,
  })
}
