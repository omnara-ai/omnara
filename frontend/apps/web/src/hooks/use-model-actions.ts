import { useDeleteConfiguredModel } from '@omnara/react'
import { ApiError } from '@omnara/sdk'

import type { ModelDialog } from '@/components/overview/ModelManagement'
import type { ModelActions } from '@/components/overview/ProviderModelList'

/** Row actions for configured models; empty for viewers who can't manage the org. */
export function useModelActions(
  orgId: string,
  canManage: boolean,
  open: (dialog: ModelDialog) => void,
): ModelActions {
  const deleteModel = useDeleteConfiguredModel(orgId)
  if (!canManage) return {}
  return {
    onCreate: (providerId) => {
      open({ kind: 'create-model', providerId })
    },
    onEdit: (model) => {
      open({ kind: 'edit-model', model })
    },
    onGrant: (model) => {
      open({ kind: 'grant-model', model })
    },
    onDelete: (provider, model) => {
      if (!window.confirm(`Delete configured model ${model.name}?`)) return
      deleteModel.mutate(
        { modelProviderConfigID: provider.id, configuredModelID: model.id },
        {
          onError: (error) => {
            window.alert(
              error instanceof ApiError ? error.message : 'Could not delete configured model',
            )
          },
        },
      )
    },
  }
}
