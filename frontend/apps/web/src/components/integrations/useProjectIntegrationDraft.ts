import { useCreateProjectIntegration, useProjectIntegration } from '@omnara/react'
import { ApiError, type IntegrationType, type ProjectIntegration } from '@omnara/sdk'
import { useState } from 'react'

import {
  projectIntegrationFormRequest,
  projectIntegrationFormValues,
} from './projectIntegrationFormState'

export function useProjectIntegrationDraft(
  orgId: string,
  projectId: string,
  integrationType: IntegrationType,
  existing?: ProjectIntegration,
) {
  const create = useCreateProjectIntegration(orgId, projectId)
  const refreshed = useProjectIntegration(orgId, projectId, existing ? '' : (create.data?.id ?? ''))
  const integration = existing ?? refreshed.data ?? create.data
  const [name, setName] = useState(() => existing?.name ?? integrationType.replaceAll('_', '-'))
  async function ensureIntegration() {
    if (integration) return integration
    try {
      return await create.mutateAsync(
        projectIntegrationFormRequest(integrationType, {
          ...projectIntegrationFormValues(integrationType),
          name,
        }),
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
