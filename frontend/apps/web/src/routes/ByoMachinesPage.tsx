import { useDeleteMachine, useGrantMachineToProject } from '@omnara/react'
import { ApiError, type VisibleMachine } from '@omnara/sdk'
import { useState } from 'react'

import { AgentCard, AgentCardGlyph } from '@/components/agents/AgentCardList'
import { Monitor } from '@/components/icons'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { ConnectMachineDialog } from '@/components/org/ConnectMachineDialog'
import { MachinesTable } from '@/components/overview/MachinesTable'
import { GrantToProjectDialog } from '@/components/projects/GrantToProjectDialog'
import { Button } from '@/components/ui/button'
import { guides } from '@/lib/docs'
import { useActiveOrg } from '@/lib/use-active-org'

export function ByoMachinesPage() {
  const { activeOrg } = useActiveOrg()
  const deleteMachine = useDeleteMachine(activeOrg.id)
  const grantMachine = useGrantMachineToProject(activeOrg.id)
  const [connectOpen, setConnectOpen] = useState(false)
  const [grantTarget, setGrantTarget] = useState<VisibleMachine | null>(null)

  const connectButton = (
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
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'machines', label: 'Machines', to: '/machines' },
          { id: 'byo', label: 'BYO Machines' },
        ]}
      />
      <AgentCard
        icon={
          <AgentCardGlyph>
            <Monitor aria-hidden="true" />
          </AgentCardGlyph>
        }
        title={<h1 className="truncate font-medium">BYO Machines</h1>}
        subtitle={<span className="truncate">Machines you connect and run yourself</span>}
        meta={null}
      />
      <MachinesTable
        orgId={activeOrg.id}
        filters={{ source_kind: 'byo' }}
        description="Machines that aren't part of any pool"
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
        emptyMessage="No machines connected yet. Connect a machine you operate to run agents on it."
        emptyAction={connectButton}
      />
      <ConnectMachineDialog open={connectOpen} onOpenChange={setConnectOpen} orgId={activeOrg.id} />
      {grantTarget && (
        <GrantToProjectDialog
          open
          onOpenChange={(open) => {
            if (!open) setGrantTarget(null)
          }}
          orgId={activeOrg.id}
          resourceName={grantTarget.display_name}
          isProjectEligible={(project) => project.access.can_manage_access}
          onGrant={(projectID) =>
            grantMachine.mutateAsync({ projectID, machineID: grantTarget.id })
          }
        />
      )}
    </div>
  )
}
