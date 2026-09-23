import { useOmnaraClient, useProjectAppOAuthCompletion } from '@omnara/react'
import { ApiError, type AppOAuthSetup, type GetProjectAppError, type ProjectApp } from '@omnara/sdk'
import { listProjectAppsQueryKey } from '@omnara/sdk/tanstack'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'

import { slackOAuthErrorDescription } from './slackOAuthErrors'

export function useSlackAuthorization(
  orgId: string,
  projectId: string,
  onConnected?: (app: ProjectApp) => void,
) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  const [pending, setPending] = useState<AppOAuthSetup>()
  const completion = useProjectAppOAuthCompletion(orgId, projectId, pending?.app_id ?? '', pending)
  const connected =
    pending &&
    completion.data?.state === 'active' &&
    completion.data.last_oauth_flow_id === pending.flow_id
      ? completion.data
      : undefined
  useEffect(() => {
    if (!connected) return
    let canceled = false
    void cache
      .invalidateQueries({
        queryKey: listProjectAppsQueryKey({ path: { orgID: orgId, projectID: projectId }, client }),
      })
      .then(() => {
        if (canceled) return
        setPending(undefined)
        onConnected?.(connected)
      })
    return () => {
      canceled = true
    }
  }, [connected, cache, client, orgId, projectId, onConnected])
  return {
    pending,
    start: setPending,
    failure: authorizationFailure(pending, Boolean(connected), completion.data, completion.error),
    checkFailed: completion.isError,
    recheck: () => void completion.refetch(),
  }
}

function authorizationFailure(
  pending: AppOAuthSetup | undefined,
  connected: boolean,
  app: ProjectApp | undefined,
  error: GetProjectAppError | null,
) {
  if (!pending || connected) return ''
  if (error instanceof ApiError && error.status === 404)
    return slackOAuthErrorDescription('app_deleted')
  if (app && app.setup_revision > pending.setup_revision)
    return slackOAuthErrorDescription('app_setup_changed')
  return ''
}
