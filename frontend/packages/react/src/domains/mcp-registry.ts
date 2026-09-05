import type { ListMcpServersData, McpRegistryServer } from '@omnara/sdk'
import { listMcpServersInfiniteOptions, listMcpServersOptions } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { DEFAULT_LIST_PAGE_SIZE, type ListFilters } from './list-options'
import { cursorPaginated } from './pagination'

export type ServerListFilters = ListFilters<ListMcpServersData>

export interface ServerListOptions {
  filters?: ServerListFilters
  pageSize?: number
  enabled?: boolean
}

export function useServers(options?: ServerListOptions) {
  const client = useOmnaraClient()
  const { filters, pageSize = DEFAULT_LIST_PAGE_SIZE, enabled = true } = options ?? {}
  return useInfiniteQuery({
    ...cursorPaginated(
      listMcpServersInfiniteOptions({ query: { ...filters, limit: pageSize }, client }),
    ),
    enabled,
  })
}

export function normalizeRemoteUrl(url: string) {
  return url
    .trim()
    .replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
    .replace(/\/+$/, '')
    .toLowerCase()
}

export function findServerByRemoteUrl(servers: McpRegistryServer[], url: string) {
  const needle = normalizeRemoteUrl(url)
  if (needle === '') return null
  return (
    servers.find((server) =>
      server.remotes.some((remote) => normalizeRemoteUrl(remote.url) === needle),
    ) ?? null
  )
}

const serverInfoStaleTime = 5 * 60 * 1000

function serverInfoQueryOptions(client: ReturnType<typeof useOmnaraClient>, remoteUrl: string) {
  return {
    ...listMcpServersOptions({ query: { remote_url: remoteUrl, limit: 5 }, client }),
    staleTime: serverInfoStaleTime,
  }
}

export function useServerInfo(url: string, options?: { enabled?: boolean }) {
  const client = useOmnaraClient()
  const remoteUrl = normalizeRemoteUrl(url)
  return useQuery({
    ...serverInfoQueryOptions(client, remoteUrl),
    select: (page): McpRegistryServer | null => findServerByRemoteUrl(page.data, remoteUrl),
    enabled: (options?.enabled ?? true) && remoteUrl !== '',
  })
}

export function useServerInfoLookup() {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return async (url: string): Promise<McpRegistryServer | null> => {
    const remoteUrl = normalizeRemoteUrl(url)
    if (remoteUrl === '') return null
    const page = await queryClient.fetchQuery(serverInfoQueryOptions(client, remoteUrl))
    return findServerByRemoteUrl(page.data, remoteUrl)
  }
}
