import { useState } from 'react'

import { CreateMachinePoolDialog } from '@/components/org/CreateMachinePoolDialog'
import { Button } from '@/components/ui/button'
import { canManageMachineGrants } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "New pool" trigger for the current project. Pools belong to the org,
 * so this creates one there and shares it with the project. Renders nothing unless the
 * viewer can manage both project access and the org.
 */
export function CreateMachinePoolButton() {
  const { activeOrg } = useActiveOrg()
  const { projectId, project } = useProjectPage()
  const [open, setOpen] = useState(false)
  if (!canManageMachineGrants(activeOrg.role, project?.access)) return null

  return (
    <>
      <Button
        type="button"
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
      >
        New pool
      </Button>
      <CreateMachinePoolDialog
        open={open}
        onOpenChange={setOpen}
        orgId={activeOrg.id}
        defaultProjectIds={[projectId]}
      />
    </>
  )
}
