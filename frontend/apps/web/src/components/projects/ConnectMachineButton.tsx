import { useState } from 'react'

import { agentCardActionClass } from '@/components/agents/AgentCardList'
import { ConnectMachineDialog } from '@/components/org/ConnectMachineDialog'
import { Button } from '@/components/ui/button'
import { canManageMachineGrants } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "Connect machine" trigger for the current project: registers a machine
 * you operate with the org and shares it with the project. Renders nothing unless the
 * viewer can manage both project access and the org. `inRow` styles it as a card row's
 * action, like Launch on a profile.
 */
export function ConnectMachineButton({ inRow = false }: { inRow?: boolean }) {
  const { activeOrg } = useActiveOrg()
  const { projectId, project } = useProjectPage()
  const [open, setOpen] = useState(false)
  if (!canManageMachineGrants(activeOrg.role, project?.access)) return null

  return (
    <>
      <Button
        type="button"
        variant={inRow ? 'ghost' : undefined}
        size="sm"
        className={inRow ? agentCardActionClass : undefined}
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
