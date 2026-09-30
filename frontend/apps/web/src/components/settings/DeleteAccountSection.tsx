import { useDeleteCurrentUser, useMe } from '@omnara/react'
import { useState } from 'react'

import { DangerZone } from '@/components/settings/DangerZone'
import { TypeToConfirmDialog } from '@/components/settings/TypeToConfirmDialog'
import { Button } from '@/components/ui/button'
import { errorMessage } from '@/lib/submit-status'

export function DeleteAccountSection() {
  const { data: me } = useMe()
  const deleteAccount = useDeleteCurrentUser()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const ownedOrgs = me.orgs.filter((org) => org.role === 'owner')
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
        description="Permanently delete your account, personal secrets and skills, and all of your access tokens and memberships."
        action={
          <Button
            type="button"
            variant="destructive"
            size="sm"
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
            <p>
              Organizations, and the agents and other resources you created in them, are kept.
              {ownedOrgs.length > 0 &&
                ' You cannot delete your account while you are the only owner of an organization: add another owner or delete the organization first.'}
            </p>
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
