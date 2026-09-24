import { useOmnaraClient, useProjectIntegrationOAuthCompletion } from '@omnara/react'
import {
  ApiError,
  type GetProjectIntegrationError,
  type IntegrationOAuthSetup,
  type ProjectIntegration,
} from '@omnara/sdk'
import { listProjectIntegrationsQueryKey } from '@omnara/sdk/tanstack'
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'

import { slackOAuthErrorDescription } from './slackOAuthErrors'

export function useSlackAuthorization(
  orgId: string,
  projectId: string,
  onConnected?: (integration: ProjectIntegration) => void,
) {
  const client = useOmnaraClient()
  const cache = useQueryClient()
  const [pending, setPending] = useState<IntegrationOAuthSetup>()
  const completion = useProjectIntegrationOAuthCompletion(
    orgId,
    projectId,
    pending?.integration_id ?? '',
    pending,
  )
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
        queryKey: listProjectIntegrationsQueryKey({
          path: { orgID: orgId, projectID: projectId },
          client,
        }),
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
  pending: IntegrationOAuthSetup | undefined,
  connected: boolean,
  integration: ProjectIntegration | undefined,
  error: GetProjectIntegrationError | null,
) {
  if (!pending || connected) return ''
  if (error instanceof ApiError && error.status === 404)
    return slackOAuthErrorDescription('integration_deleted')
  if (integration && integration.setup_revision > pending.setup_revision)
    return slackOAuthErrorDescription('integration_setup_changed')
  return ''
}
