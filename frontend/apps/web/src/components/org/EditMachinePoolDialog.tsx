import { useUpdateMachinePool } from '@omnara/react'
import { type MachinePool } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FieldGroup } from '@/components/ui/field'
import { errorMessage } from '@/lib/submit-status'

import { MachinePoolAdvancedSection } from './MachinePoolAdvancedSection'
import {
  machinePoolFormFromPool,
  machinePoolFormValid,
  type MachinePoolFormValues,
  machinePoolUpdateRequest,
} from './MachinePoolDialogState'
import { MachinePoolFields, type MachinePoolFormSetValue } from './MachinePoolFields'

export function EditMachinePoolDialog({
  open,
  onOpenChange,
  orgId,
  pool,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  pool: MachinePool
}) {
  const updateMachinePool = useUpdateMachinePool(orgId)
  const mode = pool.management_kind === 'cluster' ? 'cluster-edit' : 'tenant-edit'
  const [values, setValues] = useState<MachinePoolFormValues | null>(() =>
    machinePoolFormFromPool(pool),
  )
  const [error, setError] = useState('')

  const setValue: MachinePoolFormSetValue = (key, value) => {
    setValues((previous) => (previous === null ? null : { ...previous, [key]: value }))
  }

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (values === null) return
    setError('')
    try {
      await updateMachinePool.mutateAsync({
        poolID: pool.id,
        ...machinePoolUpdateRequest(pool, values),
      })
      onOpenChange(false)
    } catch (err) {
      setError(errorMessage(err, 'Could not update machine pool'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Edit machine pool</DialogTitle>
          <DialogDescription>Update the machines this pool provisions.</DialogDescription>
        </DialogHeader>
        {values === null ? (
          <p className="text-destructive text-sm">
            This machine pool uses an unsupported provider and cannot be edited here.
          </p>
        ) : (
          <form onSubmit={(event) => void submit(event)}>
            <FieldGroup>
              {pool.management_kind === 'cluster' && (
                <p className="text-muted-foreground text-sm">
                  Provider configuration and pool-wide quotas are managed by the cluster.
                </p>
              )}
              <MachinePoolFields
                orgId={orgId}
                enabled={open}
                mode={mode}
                values={values}
                setValue={setValue}
              />
              <MachinePoolAdvancedSection
                orgId={orgId}
                enabled={open}
                clusterManaged={mode === 'cluster-edit'}
                values={values}
                setValue={setValue}
              />
              {error && <p className="text-destructive text-sm">{error}</p>}
              <DialogFooter>
                <Button
                  type="submit"
                  disabled={updateMachinePool.isPending || !machinePoolFormValid(values, mode)}
                  loading={updateMachinePool.isPending}
                >
                  Save changes
                </Button>
              </DialogFooter>
            </FieldGroup>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
