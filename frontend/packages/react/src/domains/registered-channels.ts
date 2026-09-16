import { sdk } from '@omnara/sdk'
import {
  listRegisteredChannelsInfiniteOptions,
  listRegisteredChannelsQueryKey,
} from '@omnara/sdk/tanstack'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { cursorPaginated, DEFAULT_LIST_PAGE_SIZE } from './pagination'
import { useScopedMutation } from './scoped-mutation'

export function useRegisteredChannels(
  orgID: string,
  projectID: string,
  integrationInstallID: string,
) {
  const client = useOmnaraClient()
  return useInfiniteQuery({
    ...cursorPaginated(
      listRegisteredChannelsInfiniteOptions({
        path: { orgID, projectID, integrationInstallID },
        query: { limit: DEFAULT_LIST_PAGE_SIZE },
        client,
      }),
    ),
    select: (data) => ({
      ...data,
      pages: data.pages.map((page) => ({ ...page, data: page.channels })),
    }),
  })
}

export function useRegisterChannel(orgID: string, projectID: string, integrationInstallID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useScopedMutation(
    sdk.registerChannel,
    { orgID, projectID, integrationInstallID },
    {
      onSuccess: () =>
        queryClient.invalidateQueries({
          queryKey: listRegisteredChannelsQueryKey({
            path: { orgID, projectID, integrationInstallID },
            client,
          }),
        }),
    },
  )
}
