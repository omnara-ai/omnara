import { useInviteMember } from '@omnara/react'
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
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, success } from '@/lib/submit-status'

const roles = ['member', 'admin'] as const

interface InviteMemberState {
  email: string
  role: (typeof roles)[number]
  status: SubmitStatus
}

export function InviteMemberDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const inviteMember = useInviteMember(orgId)
  const [state, setState] = useState<InviteMemberState>({
    email: '',
    role: 'member',
    status: idle,
  })
  const errorMessage = statusError(state.status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: idle }))
    try {
      await inviteMember.mutateAsync({ email: state.email, role: state.role })
      setState((prev) => ({ ...prev, email: '', status: success }))
    } catch (err) {
      setState((prev) => ({ ...prev, status: submitError(err, 'Could not send invitation') }))
    }
  }

  function handleOpenChange(next: boolean) {
    onOpenChange(next)
    if (!next) {
      setState((prev) => ({ ...prev, status: idle }))
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Invite members</DialogTitle>
          <DialogDescription>
            They will get an email with a link to join this organization.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            void submit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="invite-email">Email</FieldLabel>
              <Input
                id="invite-email"
                type="email"
                required
                value={state.email}
                placeholder="teammate@example.com"
                autoComplete="off"
                onChange={(event) => {
                  setState((prev) => ({ ...prev, email: event.target.value }))
                }}
              />
            </Field>
            <Field>
              <FieldLabel>Role</FieldLabel>
              <div className="flex gap-2">
                {roles.map((option) => (
                  <Button
                    key={option}
                    type="button"
                    variant={state.role === option ? 'default' : 'outline'}
                    className="flex-1 capitalize"
                    onClick={() => {
                      setState((prev) => ({ ...prev, role: option }))
                    }}
                  >
                    {option}
                  </Button>
                ))}
              </div>
            </Field>
            {state.status.phase === 'success' && (
              <p className="text-muted-foreground text-sm">Invitation sent.</p>
            )}
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button type="submit" disabled={inviteMember.isPending || state.email.trim() === ''}>
                {inviteMember.isPending && <Spinner />}
                Send invitation
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
