import { useUpdateSecret } from '@omnara/react'
import { ApiError, type Secret } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

export function EditSecretDialog({
  open,
  onOpenChange,
  orgId,
  secret,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  secret: Secret
}) {
  const mutation = useUpdateSecret(orgId)
  const [name, setName] = useState(secret.name)
  const [error, setError] = useState('')

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    try {
      await mutation.mutateAsync({ secretID: secret.id, name: name.trim() })
      onOpenChange(false)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not update secret')
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit secret</DialogTitle>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <Field>
              <FieldLabel>Name</FieldLabel>
              <Input
                value={name}
                onChange={(event) => {
                  setName(event.target.value)
                }}
              />
            </Field>
            {error && <p className="text-destructive text-sm">{error}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={mutation.isPending || name.trim() === ''}
                loading={mutation.isPending}
              >
                Save changes
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
