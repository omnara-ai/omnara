import { useCreateConfiguredModel, useCreateProjectModelGrant } from '@omnara/react'
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'

import { useProjectShares } from '@/hooks/use-project-shares'
import { errorMessage } from '@/lib/submit-status'

import {
  type ConfiguredModelDraft,
  configuredModelDraftRequest,
} from './CreateConfiguredModelDialogState'

/**
 * Creating the picked models and sharing them with the chosen projects, keeping the
 * shares that failed so they can be retried without creating anything twice.
 */
export function useConfiguredModelSubmission(orgId: string, onDone: () => void) {
  const createConfiguredModel = useCreateConfiguredModel(orgId)
  const createProjectModelGrant = useCreateProjectModelGrant(orgId)
  const shares = useProjectShares(async ({ resourceId, projectId }) => {
    await createProjectModelGrant.mutateAsync({
      projectID: projectId,
      configured_model_id: resourceId,
    })
  })
  const [addedCount, setAddedCount] = useState(0)
  const [createError, setCreateError] = useState('')

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
      const shared = await shares.share(created.map((model) => model.id))
      if (failed.length > 0) {
        setCreateError(
          `Added ${String(created.length)} of ${String(batch.length)} models. ` +
            errorMessage(firstFailure?.reason, 'The remaining models could not be added.'),
        )
      } else if (shared) onDone()
      return failed
    },
  })

  const retryShares = useMutation({
    mutationFn: async (remainingDrafts: number) => {
      if ((await shares.retry()) && remainingDrafts === 0) onDone()
    },
  })

  /** Creates every draft; resolves to the drafts that failed, which stay selected. */
  async function add(providerId: string, batch: ConfiguredModelDraft[]) {
    setCreateError('')
    return addBatch.mutateAsync({ providerId, batch })
  }

  /** Retries the failed shares; closes once they succeed and no models are left to add. */
  async function retrySharing(remainingDrafts: number) {
    setCreateError('')
    await retryShares.mutateAsync(remainingDrafts)
  }

  return {
    projectIds: shares.projectIds,
    setProjectIds: shares.setProjectIds,
    /** Selected projects whose share failed, so they can be retried or removed. */
    failedProjectIds: shares.failedProjectIds,
    retrying: shares.retrying,
    addedCount,
    error: [createError, shares.error].filter(Boolean).join(' '),
    clearError: () => {
      setCreateError('')
    },
    submitting: addBatch.isPending || retryShares.isPending,
    add,
    retrySharing,
  }
}
