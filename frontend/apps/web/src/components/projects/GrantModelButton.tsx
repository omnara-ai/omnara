import { useState } from 'react'

import { GrantProjectModelDialog } from '@/components/projects/GrantProjectModelDialog'
import { Button } from '@/components/ui/button'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "Grant models" trigger and dialog for the current project.
 * Renders nothing when the viewer can't manage project access.
 */
export function GrantModelButton() {
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
        Grant models
      </Button>
      <GrantProjectModelDialog
        open={open}
        onOpenChange={setOpen}
        orgId={activeOrg.id}
        projectId={projectId}
      />
    </>
  )
}
