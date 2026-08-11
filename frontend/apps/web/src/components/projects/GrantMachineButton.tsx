import { type VisibleMachine } from '@omnara/sdk'
import { useState } from 'react'

import { GrantProjectMachineDialog } from '@/components/projects/GrantProjectMachineDialog'
import { Button } from '@/components/ui/button'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "Grant machines" trigger and dialog for the current project.
 * Only BYO machines are grantable individually; pool machines are reached
 * through their pool's grant. Renders nothing when the viewer can't manage
 * project access.
 */
export function GrantMachineButton({
  onGranted,
}: {
  onGranted?: (machines: VisibleMachine[]) => void
} = {}) {
  const { activeOrg } = useActiveOrg()
  const { projectId, project } = useProjectPage()
  const [open, setOpen] = useState(false)
  if (!project?.access.can_manage_access) return null

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
        Grant machines
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
