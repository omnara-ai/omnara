import { useDeleteOrganization } from '@omnara/react'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'

import { DangerZone } from '@/components/settings/DangerZone'
import { TypeToConfirmDialog } from '@/components/settings/TypeToConfirmDialog'
import { Button } from '@/components/ui/button'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'

export function DeleteOrganizationSection() {
  const navigate = useNavigate()
  const { orgs, activeOrg, setActiveOrgId } = useActiveOrg()
  const deleteOrganization = useDeleteOrganization()
  const [confirmOpen, setConfirmOpen] = useState(false)
  // Pin the org being deleted: the active org falls back to another one as soon
  // as the delete succeeds and the cached org list drops it.
  const [target, setTarget] = useState(activeOrg)
  const isOwner = activeOrg.role === 'owner'
  const error = deleteOrganization.isError
    ? errorMessage(deleteOrganization.error, 'Could not delete this organization.')
    : null

  async function confirmDelete() {
    const nextOrg = orgs.find((org) => org.id !== target.id)
    try {
      await deleteOrganization.mutateAsync(target.id)
    } catch {
      return
    }
    setConfirmOpen(false)
    if (nextOrg) {
      setActiveOrgId(nextOrg.id)
      await navigate({ to: '/', replace: true })
    } else {
      await navigate({ to: '/onboarding', replace: true })
    }
  }

  return (
    <>
      <DangerZone
        title="Delete organization"
        description={
          isOwner
            ? 'Permanently delete this organization and all of its projects, agents, machines, models, secrets, and skills.'
            : 'Only organization owners can delete an organization.'
        }
        action={
          <Button
            type="button"
            variant="destructive"
            size="sm"
            disabled={!isOwner}
            onClick={() => {
              deleteOrganization.reset()
              setTarget(activeOrg)
              setConfirmOpen(true)
            }}
          >
            Delete organization
          </Button>
        }
      />
      <TypeToConfirmDialog
        key={target.id}
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={`Delete ${target.name}?`}
        description={
          <>
            <p>
              This permanently deletes the organization and everything in it: projects, agents and
              their history, machines and machine pools, models, secrets, skills, API keys, and
              pending invitations. Running agents are stopped and managed machines are torn down.
            </p>
            <p>All members lose access. This cannot be undone.</p>
          </>
        }
        confirmationText={target.name}
        confirmLabel="Delete organization"
        pending={deleteOrganization.isPending}
        error={error}
        onConfirm={() => {
          void confirmDelete()
        }}
      />
    </>
  )
}
