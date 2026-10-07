import { useCreateIntegration, useIntegration } from '@omnara/react'
import {
  ApiError,
  type Integration,
  type IntegrationKind,
  profileIntegrationSetup,
} from '@omnara/sdk'
import { useState } from 'react'

import { integrationCatalog } from './integrationDefinitions'

export function useIntegrationDraft(
  orgId: string,
  projectId: string,
  integrationKind: IntegrationKind,
  existing?: Integration,
) {
  const create = useCreateIntegration(orgId, projectId)
  const refreshed = useIntegration(orgId, projectId, existing ? '' : (create.data?.id ?? ''))
  const integration = existing ?? refreshed.data ?? create.data
  const [name, setName] = useState(
    () =>
      existing?.name ??
      integrationCatalog.find((entry) => entry.integrationKind === integrationKind)?.defaultName ??
      '',
  )
  async function ensureIntegration() {
    if (integration) return integration
    try {
      return await create.mutateAsync(
        profileIntegrationSetup({ integrationKind, name, launcher: false }),
      )
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409)
        throw new Error(
          `${cause.message}. If you started setup earlier, open it from Integrations to continue.`,
        )
      throw cause
    }
  }
  return { integration, name, setName, ensureIntegration }
}
