import { useCreateProjectModelGrant } from '@omnara/react'
import { ApiError } from '@omnara/sdk'
import { useState } from 'react'

/** Shares a model with the project, tracking which one is in flight. */
export function useShareProjectModel(orgId: string, projectId: string) {
  const createGrant = useCreateProjectModelGrant(orgId)
  const [sharingId, setSharingId] = useState<string | null>(null)
  async function share(modelId: string) {
    setSharingId(modelId)
    try {
      await createGrant.mutateAsync({ projectID: projectId, configured_model_id: modelId })
    } catch (error) {
      window.alert(error instanceof ApiError ? error.message : 'Could not share model')
    }
    setSharingId(null)
  }
  return { sharingId, share }
}
