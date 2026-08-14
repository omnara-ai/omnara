import { useAcceptInvitation, useDeclineInvitation } from '@omnara/react'
import type { OrgInvitation } from '@omnara/sdk'
import { Building2, Check, X } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Spinner } from '@/components/ui/spinner'
import { formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'

type InvitationAction =
  | { kind: 'idle' }
  | { kind: 'acting'; invitationId: string; response: 'accept' | 'decline' }
  | { kind: 'error'; message: string }

export function PendingInvitationList({
  invitations,
  onAccepted,
}: {
  invitations: OrgInvitation[]
  onAccepted?: (invitation: OrgInvitation) => void | Promise<void>
}) {
  const acceptInvitation = useAcceptInvitation()
  const declineInvitation = useDeclineInvitation()
  const [action, setAction] = useState<InvitationAction>({ kind: 'idle' })

  const actingId = action.kind === 'acting' ? action.invitationId : null
  const error = action.kind === 'error' ? action.message : null

  async function accept(invitationID: string) {
    setAction({ kind: 'acting', invitationId: invitationID, response: 'accept' })
    try {
      const invitation = await acceptInvitation.mutateAsync(invitationID)
      setAction({ kind: 'idle' })
      await onAccepted?.(invitation)
    } catch (err) {
      setAction({ kind: 'error', message: errorMessage(err, 'Could not accept invitation') })
    }
  }

  async function decline(invitationID: string) {
    setAction({ kind: 'acting', invitationId: invitationID, response: 'decline' })
    try {
      await declineInvitation.mutateAsync(invitationID)
      setAction({ kind: 'idle' })
    } catch (err) {
      setAction({ kind: 'error', message: errorMessage(err, 'Could not decline invitation') })
    }
  }

  return (
    <div className="flex flex-col gap-3">
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      )}

      {invitations.map((invitation) => {
        const accepting =
          action.kind === 'acting' &&
          action.invitationId === invitation.id &&
          action.response === 'accept'
        const declining =
          action.kind === 'acting' &&
          action.invitationId === invitation.id &&
          action.response === 'decline'

        return (
          <Card key={invitation.id}>
            <CardContent className="flex flex-col gap-4 sm:flex-row sm:items-center">
              <span className="bg-secondary text-secondary-foreground flex size-10 shrink-0 items-center justify-center rounded-lg">
                <Building2 className="size-5" />
              </span>
              <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="font-medium">Organization invitation</span>
                <span className="text-muted-foreground text-sm">
                  Join as {articleFor(invitation.org_role)}{' '}
                  <span className="capitalize">{invitation.org_role}</span>
                  {formatDateTime(invitation.created_at) && (
                    <> · Invited {formatDateTime(invitation.created_at)}</>
                  )}
                </span>
              </div>
              <div className="flex shrink-0 gap-2">
                <Button
                  size="sm"
                  disabled={actingId !== null}
                  onClick={() => {
                    void accept(invitation.id)
                  }}
                >
                  {accepting ? <Spinner /> : <Check />}
                  Accept
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={actingId !== null}
                  onClick={() => {
                    void decline(invitation.id)
                  }}
                >
                  {declining ? <Spinner /> : <X />}
                  Decline
                </Button>
              </div>
            </CardContent>
          </Card>
        )
      })}
    </div>
  )
}

function articleFor(role: string) {
  return /^[aeiou]/i.test(role) ? 'an' : 'a'
}
