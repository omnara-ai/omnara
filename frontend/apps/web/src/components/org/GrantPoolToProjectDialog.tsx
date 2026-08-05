import { useGrantMachinePoolToProject } from '@omnara/react'
import { type MachinePool } from '@omnara/sdk'
import { useState } from 'react'

import {
  emptyPoolGrantDraft,
  poolGrantCreateRequest,
  poolGrantOverridesValid,
} from '@/components/projects/GrantMachinePoolDialogState'
import { PoolGrantOverridesCollapsible } from '@/components/projects/GrantMachinePoolOverridesSection'
import { GrantToProjectDialog } from '@/components/projects/GrantToProjectDialog'

export function GrantPoolToProjectDialog({
  open,
  onOpenChange,
  orgId,
  pool,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  pool: MachinePool
}) {
  const grantPool = useGrantMachinePoolToProject(orgId)
  const [draft, setDraft] = useState(emptyPoolGrantDraft)

  return (
    <GrantToProjectDialog
      open={open}
      onOpenChange={onOpenChange}
      orgId={orgId}
      resourceName={pool.name}
      isProjectEligible={(project) => project.access.can_manage_access}
      submitDisabled={!poolGrantOverridesValid(draft)}
      options={
        <PoolGrantOverridesCollapsible
          orgId={orgId}
          enabled={open}
          idPrefix={`${pool.id}-grant`}
          pool={pool}
          values={draft}
          onChange={setDraft}
        />
      }
      onGrant={(projectID) =>
        grantPool.mutateAsync({ projectID, ...poolGrantCreateRequest(pool, draft) })
      }
    />
  )
}
