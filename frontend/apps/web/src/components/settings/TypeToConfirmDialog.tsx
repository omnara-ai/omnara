import { type ReactNode, type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

/**
 * Confirmation for irreversible actions: the confirm button stays disabled
 * until the user types `confirmationText` exactly.
 */
export function TypeToConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmationText,
  confirmLabel,
  pending,
  error,
  onConfirm,
  destructive = false,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: ReactNode
  confirmationText: string
  confirmLabel: string
  pending: boolean
  error: string | null
  onConfirm: () => void
  destructive?: boolean
}) {
  const [typed, setTyped] = useState('')
  const matches = typed === confirmationText

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (matches && !pending) onConfirm()
  }

  function handleOpenChange(nextOpen: boolean) {
    if (pending) return
    if (!nextOpen) setTyped('')
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription asChild>
            <div className="flex flex-col gap-2">{description}</div>
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="type-to-confirm">
                <span>
                  Type <span className="font-mono font-semibold">{confirmationText}</span> to
                  confirm
                </span>
              </FieldLabel>
              <Input
                id="type-to-confirm"
                value={typed}
                autoComplete="off"
                spellCheck={false}
                disabled={pending}
                onChange={(event) => {
                  setTyped(event.target.value)
                }}
              />
            </Field>
            {error && (
              <p role="alert" className="text-destructive text-sm">
                {error}
              </p>
            )}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={pending}
                onClick={() => {
                  handleOpenChange(false)
                }}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                variant={destructive ? 'destructive' : 'default'}
                disabled={!matches || pending}
                loading={pending}
              >
                {confirmLabel}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
