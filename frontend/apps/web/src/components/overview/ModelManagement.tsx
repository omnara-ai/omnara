import { useDeleteModelProvider } from '@omnara/react'
import { ApiError, type ConfiguredModel, type ModelProviderConfig } from '@omnara/sdk'

import { CreateConfiguredModelDialog } from '@/components/org/CreateConfiguredModelDialog'
import { CreateModelProviderDialog } from '@/components/org/CreateModelProviderDialog'
import { EditConfiguredModelDialog } from '@/components/org/EditConfiguredModelDialog'
import { EditModelProviderDialog } from '@/components/org/EditModelProviderDialog'
import { GrantConfiguredModelDialog } from '@/components/org/GrantConfiguredModelDialog'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'

export type ModelDialog =
  | { kind: 'create-provider' }
  | { kind: 'edit-provider'; provider: ModelProviderConfig }
  | { kind: 'create-model'; providerId?: string }
  | { kind: 'edit-model'; model: ConfiguredModel }
  | { kind: 'grant-model'; model: ConfiguredModel }
  | null

/** Edit / delete menu for an org-managed provider. Omnara-managed providers have none. */
export function ProviderActions({
  orgId,
  provider,
  onEdit,
  onDeleted,
}: {
  orgId: string
  provider: ModelProviderConfig
  onEdit: () => void
  onDeleted?: () => void
}) {
  const deleteProvider = useDeleteModelProvider(orgId)
  if (provider.management_kind !== 'tenant') return null
  return (
    <ResourceRowActions
      onEdit={onEdit}
      onDelete={() => {
        if (!window.confirm(`Delete model provider ${provider.name}?`)) return
        deleteProvider.mutate(provider.id, {
          onSuccess: onDeleted,
          onError: (error) => {
            window.alert(
              error instanceof ApiError ? error.message : 'Could not delete model provider',
            )
          },
        })
      }}
    />
  )
}

export function ModelDialogs({
  orgId,
  providers,
  dialog,
  onClose,
}: {
  orgId: string
  /** Providers offered when creating a model. */
  providers: ModelProviderConfig[]
  dialog: ModelDialog
  onClose: () => void
}) {
  const onOpenChange = (open: boolean) => {
    if (!open) onClose()
  }
  return (
    <>
      <CreateModelProviderDialog
        open={dialog?.kind === 'create-provider'}
        onOpenChange={onOpenChange}
        orgId={orgId}
      />
      {dialog?.kind === 'edit-provider' && (
        <EditModelProviderDialog
          open
          onOpenChange={onOpenChange}
          orgId={orgId}
          provider={dialog.provider}
        />
      )}
      {providers.length > 0 && (
        <CreateConfiguredModelDialog
          open={dialog?.kind === 'create-model'}
          onOpenChange={onOpenChange}
          orgId={orgId}
          providers={providers}
          defaultProviderId={dialog?.kind === 'create-model' ? dialog.providerId : undefined}
        />
      )}
      {dialog?.kind === 'edit-model' && (
        <EditConfiguredModelDialog
          open
          onOpenChange={onOpenChange}
          orgId={orgId}
          model={dialog.model}
        />
      )}
      {dialog?.kind === 'grant-model' && (
        <GrantConfiguredModelDialog
          open
          onOpenChange={onOpenChange}
          orgId={orgId}
          model={dialog.model}
        />
      )}
    </>
  )
}
