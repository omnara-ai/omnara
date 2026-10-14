import { useDeleteMachinePool } from '@omnara/react'
import { ApiError, type MachinePool } from '@omnara/sdk'

import { CreateMachinePoolDialog } from '@/components/org/CreateMachinePoolDialog'
import { EditMachinePoolDialog } from '@/components/org/EditMachinePoolDialog'
import { GrantPoolToProjectDialog } from '@/components/org/GrantPoolToProjectDialog'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'

export type MachinePoolDialog =
  | { kind: 'create' }
  | { kind: 'edit'; pool: MachinePool }
  | { kind: 'grant'; pool: MachinePool }
  | null

/** Edit / share / delete menu for a machine pool. Only org managers should see it. */
export function MachinePoolActions({
  orgId,
  pool,
  onOpen,
  onDeleted,
}: {
  orgId: string
  pool: MachinePool
  onOpen: (dialog: MachinePoolDialog) => void
  onDeleted?: () => void
}) {
  const deletePool = useDeleteMachinePool(orgId)
  return (
    <ResourceRowActions
      onEdit={() => {
        onOpen({ kind: 'edit', pool })
      }}
      onGrant={() => {
        onOpen({ kind: 'grant', pool })
      }}
      onDelete={
        pool.management_kind === 'tenant'
          ? () => {
              if (!window.confirm(`Delete machine pool ${pool.name}?`)) return
              deletePool.mutate(pool.id, {
                onSuccess: onDeleted,
                onError: (error) => {
                  window.alert(
                    error instanceof ApiError ? error.message : 'Could not delete machine pool',
                  )
                },
              })
            }
          : undefined
      }
    />
  )
}

export function MachinePoolDialogs({
  orgId,
  dialog,
  onClose,
}: {
  orgId: string
  dialog: MachinePoolDialog
  onClose: () => void
}) {
  const onOpenChange = (open: boolean) => {
    if (!open) onClose()
  }
  return (
    <>
      <CreateMachinePoolDialog
        open={dialog?.kind === 'create'}
        onOpenChange={onOpenChange}
        orgId={orgId}
      />
      {dialog?.kind === 'edit' && (
        <EditMachinePoolDialog
          key={dialog.pool.id}
          open
          onOpenChange={onOpenChange}
          orgId={orgId}
          pool={dialog.pool}
        />
      )}
      {dialog?.kind === 'grant' && (
        <GrantPoolToProjectDialog
          key={dialog.pool.id}
          open
          onOpenChange={onOpenChange}
          orgId={orgId}
          pool={dialog.pool}
        />
      )}
    </>
  )
}
