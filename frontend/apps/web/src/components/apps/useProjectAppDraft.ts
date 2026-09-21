import { useCreateProjectApp, useProjectApp } from '@omnara/react'
import { ApiError, type AppType, type ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { projectAppFormRequest, projectAppFormValues } from './projectAppFormState'

/** Create only on submission; reuse the saved app if connecting its account needs a retry. */
export function useProjectAppDraft(
  orgId: string,
  projectId: string,
  appType: AppType,
  existing?: ProjectApp,
) {
  const create = useCreateProjectApp(orgId, projectId)
  const refreshed = useProjectApp(orgId, projectId, existing ? '' : (create.data?.id ?? ''))
  const app = existing ?? refreshed.data ?? create.data
  const [name, setName] = useState(() => existing?.name ?? appType.replaceAll('_', '-'))
  async function ensureApp() {
    if (app) return app
    try {
      return await create.mutateAsync(
        projectAppFormRequest(appType, { ...projectAppFormValues(appType), name }),
      )
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409)
        throw new Error(
          `${cause.message}. If you started setup earlier, open it from Apps to continue.`,
        )
      throw cause
    }
  }
  return { app, name, setName, ensureApp }
}
