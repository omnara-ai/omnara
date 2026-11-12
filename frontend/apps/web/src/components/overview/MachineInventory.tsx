import { useDeleteMachine, useGrantMachineToProject } from '@omnara/react'
import { ApiError, type MachineSourceKind, type VisibleMachine } from '@omnara/sdk'
import { useState } from 'react'

import { ConnectMachineDialog } from '@/components/org/ConnectMachineDialog'
import { MachinesTable } from '@/components/overview/MachinesTable'
import { GrantToProjectDialog } from '@/components/projects/GrantToProjectDialog'
import { Button } from '@/components/ui/button'
import { guides } from '@/lib/docs'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

/**
 * The org's machines as a table, with Connect machine for org admins and share/delete on BYO rows
 * the viewer manages. `filters` narrows it, e.g. to BYO machines.
 */
export function MachineInventory({
  orgId,
  filters,
  description,
  emptyMessage,
}: {
  orgId: string
  filters: { source_kind?: MachineSourceKind }
  description: string
  /** Shown when there are no machines, with a Connect machine action for admins. */
  emptyMessage: { canConnect: string; cannotConnect: string }
}) {
  const { activeOrg } = useActiveOrg()
  // Connecting a machine requires org.manage.
  const canConnect = canManageOrg(activeOrg.role)
  const deleteMachine = useDeleteMachine(orgId)
  const grantMachine = useGrantMachineToProject(orgId)
  const [connectOpen, setConnectOpen] = useState(false)
  const [grantTarget, setGrantTarget] = useState<VisibleMachine | null>(null)

  const connectButton = canConnect && (
    <Button
      size="sm"
      onClick={() => {
        setConnectOpen(true)
      }}
    >
      Connect machine
    </Button>
  )

  return (
    <>
      <MachinesTable
        orgId={orgId}
        filters={filters}
        description={description}
        guide={guides.machines}
        headerAction={connectButton}
        actions={{
          onGrant: setGrantTarget,
          onDelete: (machine) => {
            if (!window.confirm(`Delete machine ${machine.display_name}?`)) return
            deleteMachine.mutate(machine.id, {
              onError: (error) => {
                window.alert(error instanceof ApiError ? error.message : 'Could not delete machine')
              },
            })
          },
        }}
        emptyMessage={canConnect ? emptyMessage.canConnect : emptyMessage.cannotConnect}
        emptyAction={connectButton}
      />
      {canConnect && (
        <ConnectMachineDialog open={connectOpen} onOpenChange={setConnectOpen} orgId={orgId} />
      )}
      {grantTarget && (
        <GrantToProjectDialog
          open
          onOpenChange={(open) => {
            if (!open) setGrantTarget(null)
          }}
          orgId={orgId}
          resourceName={grantTarget.display_name}
          isProjectEligible={(project) => project.access.can_manage_access}
          onGrant={(projectID) =>
            grantMachine.mutateAsync({ projectID, machineID: grantTarget.id })
          }
        />
      )}
    </>
  )
}
