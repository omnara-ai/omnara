import type { ListMcpServersData, McpRegistryServer } from '@omnara/sdk'
import { listMcpServersInfiniteOptions, listMcpServersOptions } from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { DEFAULT_LIST_PAGE_SIZE, type ListFilters } from './list-options'
import { cursorPagination } from './pagination'

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
    ...listMcpServersInfiniteOptions({ query: { ...filters, limit: pageSize }, client }),
    ...cursorPagination,
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

export function useServerInfo(url: string, options?: { enabled?: boolean }) {
  const client = useOmnaraClient()
  const remoteUrl = normalizeRemoteUrl(url)
  return useQuery({
    ...listMcpServersOptions({ query: { remote_url: remoteUrl, limit: 5 }, client }),
    select: (page): McpRegistryServer | null => findServerByRemoteUrl(page.data, remoteUrl),
    enabled: (options?.enabled ?? true) && remoteUrl !== '',
    staleTime: 5 * 60 * 1000,
  })
}
