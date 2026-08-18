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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, settleSubmission, statusError, submitError } from '@/lib/submit-status'

const roles = ['member', 'admin'] as const

interface InviteMemberState {
  email: string
  role: (typeof roles)[number]
  status: SubmitStatus
}

export function InviteMemberDialog({
  open,
  onOpenChange,
  onInvited,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onInvited?: () => void
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
    const result = await settleSubmission(() =>
      inviteMember.mutateAsync({ email: state.email, role: state.role }),
    )
    if (result.ok) {
      setState((prev) => ({ ...prev, email: '', status: idle }))
      onInvited?.()
      onOpenChange(false)
    } else {
      const status = submitError(result.error, 'Could not send invitation')
      setState((prev) => ({ ...prev, status }))
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
              <FieldLabel htmlFor="invite-role">Role</FieldLabel>
              <Select
                value={state.role}
                onValueChange={(role) => {
                  setState((prev) => ({
                    ...prev,
                    role: role as InviteMemberState['role'],
                  }))
                }}
              >
                <SelectTrigger id="invite-role" className="w-full capitalize">
                  <SelectValue>{state.role}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {roles.map((role) => (
                    <SelectItem key={role} value={role} className="capitalize">
                      {role}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={inviteMember.isPending || state.email.trim() === ''}
                loading={inviteMember.isPending}
              >
                Send invitation
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
