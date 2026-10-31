import { useState } from 'react'

import { ConnectMachineDialog } from '@/components/org/ConnectMachineDialog'
import { Button } from '@/components/ui/button'
import { canManageMachineGrants } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "Connect machine" trigger for the current project: registers a machine
 * you operate with the org and shares it with the project. Renders nothing unless the
 * viewer can manage both project access and the org.
 */
export function ConnectMachineButton() {
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
        Connect machine
      </Button>
      <ConnectMachineDialog
        // Keyed by project: the page stays mounted when switching projects, and the dialog's
        // share defaults are only read when it mounts.
        key={projectId}
        open={open}
        onOpenChange={setOpen}
        orgId={activeOrg.id}
        defaultProjectIds={[projectId]}
      />
    </>
  )
}
