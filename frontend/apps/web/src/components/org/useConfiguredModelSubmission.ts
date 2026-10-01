import { useCreateConfiguredModel, useCreateProjectModelGrant } from '@omnara/react'
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'

import { errorMessage } from '@/lib/submit-status'

import {
  type ConfiguredModelDraft,
  configuredModelDraftRequest,
} from './CreateConfiguredModelDialogState'

interface PendingGrant {
  modelId: string
  projectId: string
}

function sharingFailure(count: number) {
  return `Sharing failed for ${String(count)} ${count === 1 ? 'project' : 'projects'}; retry to finish sharing.`
}

/**
 * Creating the picked models and sharing them with the chosen projects, keeping the
 * shares that failed so they can be retried without creating anything twice.
 */
export function useConfiguredModelSubmission(orgId: string, onDone: () => void) {
  const createConfiguredModel = useCreateConfiguredModel(orgId)
  const createProjectModelGrant = useCreateProjectModelGrant(orgId)
  const [projectIds, setProjectIds] = useState<string[]>([])
  const [pendingGrants, setPendingGrants] = useState<PendingGrant[]>([])
  const [addedCount, setAddedCount] = useState(0)
  const [error, setError] = useState('')

  async function share(grants: PendingGrant[]) {
    const results = await Promise.allSettled(
      grants.map((grant) =>
        createProjectModelGrant.mutateAsync({
          projectID: grant.projectId,
          configured_model_id: grant.modelId,
        }),
      ),
    )
    return grants.filter((_, index) => results[index]?.status === 'rejected')
  }

  // The batch fans out to many concurrent calls of one mutation, whose own isPending
  // only tracks the latest call, so the batch itself is the mutation the UI waits on.
  const addBatch = useMutation({
    mutationFn: async ({
      providerId,
      batch,
    }: {
      providerId: string
      batch: ConfiguredModelDraft[]
    }) => {
      const results = await Promise.allSettled(
        batch.map((draft) =>
          createConfiguredModel.mutateAsync({
            modelProviderConfigID: providerId,
            ...configuredModelDraftRequest(draft),
          }),
        ),
      )
      const created = results.flatMap((result) =>
        result.status === 'fulfilled' ? [result.value] : [],
      )
      const failed = batch.filter((_, index) => results[index]?.status === 'rejected')
      const firstFailure = results.find((result) => result.status === 'rejected')
      setAddedCount((count) => count + created.length)
      const failedGrants = await share(
        created.flatMap((model) =>
          projectIds.map((projectId) => ({ modelId: model.id, projectId })),
        ),
      )
      setPendingGrants(failedGrants)
      const messages = []
      if (failed.length > 0) {
        messages.push(
          `Added ${String(created.length)} of ${String(batch.length)} models. ` +
            errorMessage(firstFailure?.reason, 'The remaining models could not be added.'),
        )
      }
      if (failedGrants.length > 0) messages.push(sharingFailure(failedGrants.length))
      if (messages.length === 0) onDone()
      else setError(messages.join(' '))
      return failed
    },
  })

  const retryShares = useMutation({
    mutationFn: async (remainingDrafts: number) => {
      const failedGrants = await share(pendingGrants)
      setPendingGrants(failedGrants)
      if (failedGrants.length > 0) setError(sharingFailure(failedGrants.length))
      else if (remainingDrafts === 0) onDone()
    },
  })

  /** Creates every draft; resolves to the drafts that failed, which stay selected. */
  async function add(providerId: string, batch: ConfiguredModelDraft[]) {
    setError('')
    return addBatch.mutateAsync({ providerId, batch })
  }

  /** Retries the failed shares; closes once they succeed and no models are left to add. */
  async function retrySharing(remainingDrafts: number) {
    setError('')
    await retryShares.mutateAsync(remainingDrafts)
  }

  return {
    projectIds,
    setProjectIds,
    retrying: pendingGrants.length > 0,
    addedCount,
    error,
    clearError: () => {
      setError('')
    },
    submitting: addBatch.isPending || retryShares.isPending,
    add,
    retrySharing,
  }
}
