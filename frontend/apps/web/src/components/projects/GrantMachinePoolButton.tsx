import { type MachinePool } from '@omnara/sdk'
import { useState } from 'react'

import { GrantMachinePoolDialog } from '@/components/projects/GrantMachinePoolDialog'
import { Button } from '@/components/ui/button'
import { canManageMachineGrants } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "Share pool" trigger and dialog for the current project.
 * Renders nothing unless the viewer can manage both project access and the org.
 */
export function GrantMachinePoolButton({
  onGranted,
}: {
  onGranted?: (pool: MachinePool) => void
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
        Share pool
      </Button>
      <GrantMachinePoolDialog
        open={open}
        onOpenChange={setOpen}
        orgId={activeOrg.id}
        projectId={projectId}
        onGranted={onGranted}
      />
    </>
  )
}
