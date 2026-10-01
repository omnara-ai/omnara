import { type ListMemoryStoresData, MAX_MEMORY_FILE_BYTES, sdk } from '@omnara/sdk'
import {
  downloadMemoryFileQueryKey,
  getMemoryStoreOptions,
  getMemoryStoreQueryKey,
  listMemoryFilesInfiniteOptions,
  listMemoryFilesQueryKey,
  listMemoryStoresInfiniteOptions,
  listMemoryStoresQueryKey,
} from '@omnara/sdk/tanstack'
import {
  type QueryClient,
  skipToken,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import * as z from 'zod'

import { useOmnaraClient } from '../omnara-client'
import { type PaginatedListOptions, paginatedListOptions } from './list-options'
import { cursorPaginated } from './pagination'
import { removeQueryWhenInactive } from './query-keys'
import { useScopedMutation } from './scoped-mutation'

const memoryFileDigest = z.object({ digest: z.string() })

export interface MemoryScope {
  orgID: string
  projectID: string
  memoryStoreID: string
}

export function useMemoryStores(
  orgID: string,
  projectID: string,
  options?: PaginatedListOptions<ListMemoryStoresData>,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listMemoryStoresInfiniteOptions({ path: { orgID, projectID }, query: list.query, client }),
    ),
    enabled: list.enabled,
  })
}

export function useMemoryStore(scope: MemoryScope) {
  const client = useOmnaraClient()
  return useQuery(getMemoryStoreOptions({ path: scope, client }))
}

export function useMemoryFiles(
  scope: MemoryScope,
  path: string,
  limitDirectoryRequests?: <T>(fn: () => T | Promise<T>) => Promise<T>,
) {
  const client = useOmnaraClient()
  const { queryFn, ...options } = cursorPaginated(
    listMemoryFilesInfiniteOptions({ path: scope, query: { path, limit: 50 }, client }),
  )
  return useInfiniteQuery({
    ...options,
    queryFn:
      limitDirectoryRequests && queryFn && queryFn !== skipToken
        ? (context) => {
            const { signal } = context
            return limitDirectoryRequests(() => {
              signal.throwIfAborted()
              return queryFn(context)
            })
          }
        : queryFn,
  })
}

function invalidateParentDirectories(
  queryClient: QueryClient,
  client: ReturnType<typeof useOmnaraClient>,
  scope: MemoryScope,
  path: string,
) {
  const parts = path.split('/')
  const requests: Promise<void>[] = []
  while (parts.length) {
    parts.pop()
    requests.push(
      queryClient.invalidateQueries({
        queryKey: listMemoryFilesQueryKey({
          path: scope,
          query: { path: parts.join('/') },
          client,
        }),
      }),
    )
  }
  return Promise.all(requests)
}

export function useMemoryFile(scope: MemoryScope, path: string) {
  const client = useOmnaraClient()
  return useQuery({
    queryKey: [...downloadMemoryFileQueryKey({ path: scope, query: { path }, client }), 'content'],
    queryFn: async ({ signal }) => {
      const { response } = await sdk.downloadMemoryFile({
        path: scope,
        query: { path },
        client,
        signal,
        parseAs: 'stream',
      })
      const digest = response.headers.get('X-Omnara-File-Digest')
      if (!digest) throw new Error('The file response is missing content or its digest')
      return { bytes: new Uint8Array(await response.arrayBuffer()), digest }
    },
    structuralSharing: (previous, next) => {
      const previousDigest = memoryFileDigest.safeParse(previous).data?.digest
      const nextDigest = memoryFileDigest.safeParse(next).data?.digest
      return previousDigest !== undefined && previousDigest === nextDigest ? previous : next
    },
    refetchOnWindowFocus: 'always',
    refetchOnReconnect: 'always',
    gcTime: 0,
  })
}

export function useCreateMemoryStore(orgID: string, projectID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.createMemoryStore,
    { orgID, projectID },
    {
      onSuccess: () =>
        queryClient.invalidateQueries({
          queryKey: listMemoryStoresQueryKey({ path: { orgID, projectID }, client }),
        }),
    },
  )
}

export function useUpdateMemoryStore(scope: MemoryScope) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(sdk.updateMemoryStore, scope, {
    onSuccess: (store) => {
      queryClient.setQueryData(getMemoryStoreQueryKey({ path: scope, client }), store)
      return queryClient.invalidateQueries({
        queryKey: listMemoryStoresQueryKey({
          path: { orgID: scope.orgID, projectID: scope.projectID },
          client,
        }),
      })
    },
  })
}

export function useDeleteMemoryStore(scope: MemoryScope) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => sdk.deleteMemoryStore({ path: scope, client }),
    onSuccess: async () => {
      removeQueryWhenInactive(queryClient, getMemoryStoreQueryKey({ path: scope, client }))
      await queryClient.invalidateQueries({
        queryKey: listMemoryStoresQueryKey({
          path: { orgID: scope.orgID, projectID: scope.projectID },
          client,
        }),
      })
    },
  })
}

export function useWriteMemoryFile(scope: MemoryScope) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({
      path,
      content,
      expectedDigest,
    }: {
      path: string
      content: Blob
      expectedDigest?: string
    }) => {
      if (content.size > MAX_MEMORY_FILE_BYTES)
        throw new Error('The file exceeds the 10 MiB limit.')
      const { data } = await sdk.writeMemoryFile({
        path: scope,
        query: { path, expected_digest: expectedDigest },
        body: content,
        client,
      })
      return data
    },
    onSuccess: async (data, { path, content }) => {
      const queryKey = downloadMemoryFileQueryKey({ path: scope, query: { path }, client })
      const bytes = new Uint8Array(await content.arrayBuffer())
      await queryClient.cancelQueries({ queryKey })
      queryClient.setQueryData([...queryKey, 'content'], { bytes, digest: data.digest })
      void invalidateParentDirectories(queryClient, client, scope, path)
    },
  })
}

export function useDeleteMemoryFile(scope: MemoryScope) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ path, digest }: { path: string; digest: string }) =>
      sdk.deleteMemoryFile({ path: scope, query: { path, expected_digest: digest }, client }),
    onSuccess: async (_data, { path }) => {
      await invalidateParentDirectories(queryClient, client, scope, path)
      removeQueryWhenInactive(queryClient, [
        ...downloadMemoryFileQueryKey({ path: scope, query: { path }, client }),
        'content',
      ])
    },
  })
}
