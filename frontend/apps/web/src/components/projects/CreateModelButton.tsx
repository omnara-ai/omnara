import { useModelProviders } from '@omnara/react'
import { useState } from 'react'

import { CreateConfiguredModelDialog } from '@/components/org/CreateConfiguredModelDialog'
import { CreateModelProviderDialog } from '@/components/org/CreateModelProviderDialog'
import { Button } from '@/components/ui/button'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

/**
 * Self-contained "New model" trigger for the current project. Models belong to the org,
 * so this creates them there and shares them with the project; without a provider yet,
 * it starts by adding one. Renders nothing unless the viewer can manage both project
 * access and the org.
 */
export function CreateModelButton() {
  const { activeOrg } = useActiveOrg()
  const { projectId, project } = useProjectPage()
  const canCreate = canManageOrg(activeOrg.role) && (project?.access.can_manage_access ?? false)
  const providersQuery = useModelProviders(activeOrg.id, { enabled: canCreate })
  const providers = providersQuery.data?.pages.flatMap((page) => page.data) ?? []
  const [open, setOpen] = useState(false)
  if (!canCreate) return null

  const defaultProjectIds = [projectId]
  return (
    <>
      <Button
        type="button"
        size="sm"
        disabled={providersQuery.isPending}
        onClick={() => {
          setOpen(true)
        }}
      >
        New model
      </Button>
      {providers.length > 0 ? (
        <CreateConfiguredModelDialog
          open={open}
          onOpenChange={setOpen}
          orgId={activeOrg.id}
          providers={providers}
          defaultProjectIds={defaultProjectIds}
        />
      ) : (
        <CreateModelProviderDialog
          open={open}
          onOpenChange={setOpen}
          orgId={activeOrg.id}
          defaultProjectIds={defaultProjectIds}
        />
      )}
    </>
  )
}
