import { useAcceptInvitation, useDeclineInvitation } from '@omnara/react'
import type { OrgInvitation } from '@omnara/sdk'
import { Building2, Check, Copy, X } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
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
  const [copiedInvitationID, setCopiedInvitationID] = useState<string | null>(null)
  const [copyError, setCopyError] = useState<string | null>(null)

  const actingId = action.kind === 'acting' ? action.invitationId : null
  const error = action.kind === 'error' ? action.message : copyError

  useEffect(() => {
    if (copiedInvitationID === null) return

    const timeout = window.setTimeout(() => {
      setCopiedInvitationID(null)
    }, 1500)

    return () => {
      window.clearTimeout(timeout)
    }
  }, [copiedInvitationID])

  async function accept(invitationID: string) {
    setAction({ kind: 'acting', invitationId: invitationID, response: 'accept' })
    let invitation: OrgInvitation
    try {
      invitation = await acceptInvitation.mutateAsync(invitationID)
    } catch (err) {
      setAction({ kind: 'error', message: errorMessage(err, 'Could not accept invitation') })
      return
    }

    try {
      await onAccepted?.(invitation)
    } catch (err) {
      setAction({
        kind: 'error',
        message: errorMessage(err, 'Invitation accepted, but the page could not refresh'),
      })
      return
    }
    setAction({ kind: 'idle' })
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

  async function copyOrganizationID(invitation: OrgInvitation) {
    try {
      await navigator.clipboard.writeText(invitation.org_id)
      setCopyError(null)
      setCopiedInvitationID(invitation.id)
    } catch {
      setCopyError('Could not copy organization ID')
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
              <div className="flex min-w-0 flex-1 flex-col">
                <div className="flex min-w-0 flex-col">
                  <span className="truncate font-medium leading-5">{invitation.org_name}</span>
                  <button
                    type="button"
                    className="text-muted-foreground hover:text-foreground focus-visible:ring-ring/50 -ml-1 flex w-fit min-w-0 max-w-full items-center gap-1 rounded px-1 text-left text-xs leading-4 transition-colors focus-visible:outline-none focus-visible:ring-2"
                    aria-label={
                      copiedInvitationID === invitation.id
                        ? `Copied organization ID ${invitation.org_id}`
                        : `Copy organization ID ${invitation.org_id}`
                    }
                    title={copiedInvitationID === invitation.id ? 'Copied' : 'Copy organization ID'}
                    onClick={() => void copyOrganizationID(invitation)}
                  >
                    <code className="truncate font-mono text-[11px] font-normal">
                      {invitation.org_id}
                    </code>
                    {copiedInvitationID === invitation.id ? (
                      <Check className="size-3 shrink-0" />
                    ) : (
                      <Copy className="size-3 shrink-0" />
                    )}
                  </button>
                </div>
                <span className="text-muted-foreground mt-1.5 text-sm">
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
                  aria-label={`Accept invitation to ${invitation.org_name}`}
                  disabled={actingId !== null}
                  loading={accepting}
                  icon={<Check />}
                  onClick={() => {
                    void accept(invitation.id)
                  }}
                >
                  Accept
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={`Decline invitation to ${invitation.org_name}`}
                  disabled={actingId !== null}
                  loading={declining}
                  icon={<X />}
                  onClick={() => {
                    void decline(invitation.id)
                  }}
                >
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
