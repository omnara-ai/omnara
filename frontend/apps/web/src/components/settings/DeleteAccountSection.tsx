import { useDeleteCurrentUser, useMe } from '@omnara/react'
import type { CurrentUserOrg } from '@omnara/sdk'
import { useState } from 'react'

import { DangerZone } from '@/components/settings/DangerZone'
import { TypeToConfirmDialog } from '@/components/settings/TypeToConfirmDialog'
import { Button } from '@/components/ui/button'
import { errorMessage } from '@/lib/submit-status'

const listFormat = new Intl.ListFormat('en')

/** "A, B, and C" with each org name bolded. */
function OrgNameList({ orgs }: { orgs: CurrentUserOrg[] }) {
  const byId = new Map(orgs.map((org) => [org.id, org]))
  return listFormat.formatToParts(orgs.map((org) => org.id)).map((part) =>
    part.type === 'element' ? (
      <strong key={part.value} className="text-foreground font-medium">
        {byId.get(part.value)?.name}
      </strong>
    ) : (
      part.value
    ),
  )
}

export function DeleteAccountSection() {
  const { data: me } = useMe()
  const deleteAccount = useDeleteCurrentUser()
  const [confirmOpen, setConfirmOpen] = useState(false)
  // Owners can't be added or transferred, so the server rejects deleting any account that owns an org.
  const ownedOrgs = me.orgs.filter((org) => org.role === 'owner')
  const ownsOrgs = ownedOrgs.length > 0
  const error = deleteAccount.isError
    ? errorMessage(deleteAccount.error, 'Could not delete your account.')
    : null

  async function confirmDelete() {
    try {
      await deleteAccount.mutateAsync()
    } catch {
      return
    }
    // The server revoked this browser session; a full navigation drops all cached state.
    window.location.href = '/login'
  }

  return (
    <>
      <DangerZone
        title="Delete account"
        description={
          ownsOrgs ? (
            <>
              You own <OrgNameList orgs={ownedOrgs} />. Delete{' '}
              {ownedOrgs.length === 1 ? 'that organization' : 'those organizations'} from
              organization settings before deleting your account.
            </>
          ) : (
            'Permanently delete your account, personal secrets and skills, and all of your access tokens and memberships.'
          )
        }
        action={
          <Button
            type="button"
            variant="destructive"
            size="sm"
            disabled={ownsOrgs}
            onClick={() => {
              deleteAccount.reset()
              setConfirmOpen(true)
            }}
          >
            Delete account
          </Button>
        }
      />
      <TypeToConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title="Delete your account?"
        description={
          <>
            <p>
              This signs you out everywhere, revokes your personal access tokens, removes you from
              every organization and project, and deletes your personal secrets and skills. Your
              email is released so it can be used to register again.
            </p>
            <p>Organizations, and the agents and other resources you created in them, are kept.</p>
            <p>This cannot be undone.</p>
          </>
        }
        confirmationText={me.user.email || 'delete my account'}
        confirmLabel="Delete account"
        pending={deleteAccount.isPending}
        error={error}
        onConfirm={() => {
          void confirmDelete()
        }}
      />
    </>
  )
}
