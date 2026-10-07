import { type VisibleMachine } from '@omnara/sdk'
import { useState } from 'react'

import { GrantProjectMachineDialog } from '@/components/projects/GrantProjectMachineDialog'
import { Button } from '@/components/ui/button'
import { canManageMachineGrants } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "Share machines" trigger and dialog for the current project.
 * Only BYO machines are grantable individually; pool machines are reached
 * through their pool's grant. Renders nothing unless the viewer can manage
 * both project access and the org.
 */
export function GrantMachineButton({
  onGranted,
}: {
  onGranted?: (machines: VisibleMachine[]) => void
} = {}) {
  const { activeOrg } = useActiveOrg()
  const { projectId, project } = useProjectPage()
  const [open, setOpen] = useState(false)
  if (!canManageMachineGrants(activeOrg.role, project?.access)) return null

  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
      >
        Share machines
      </Button>
      <GrantProjectMachineDialog
        open={open}
        onOpenChange={setOpen}
        orgId={activeOrg.id}
        projectId={projectId}
        onGranted={onGranted}
      />
    </>
  )
}
