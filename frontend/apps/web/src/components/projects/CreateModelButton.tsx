import { useModelProviders } from '@omnara/react'
import { type ComponentProps, type ReactNode, useState } from 'react'

import { CreateConfiguredModelDialog } from '@/components/org/CreateConfiguredModelDialog'
import { CreateModelProviderDialog } from '@/components/org/CreateModelProviderDialog'
import { Button } from '@/components/ui/button'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
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
  if (!canCreate) return null
  return (
    <ProjectModelCreateButton orgId={activeOrg.id} projectId={projectId} size="sm">
      New model
    </ProjectModelCreateButton>
  )
}

/** The same trigger for any project the viewer can share org models with. */
export function ProjectModelCreateButton({
  orgId,
  projectId,
  children,
  ...button
}: {
  orgId: string
  projectId: string
  children: ReactNode
} & Pick<ComponentProps<typeof Button>, 'size' | 'variant' | 'className'>) {
  // The provider picker only sees loaded items, so load every page before offering it.
  const providersQuery = useModelProviders(orgId, { pageSize: 100 })
  const { items: providers, hasCompleteItems } = useCompleteInfiniteQueryItems(providersQuery, true)
  const [open, setOpen] = useState(false)
  // Chosen on click and kept until the next one: creating the first provider refreshes the
  // provider list, which must not swap the open wizard for the model dialog.
  const [startWithProvider, setStartWithProvider] = useState(false)

  const defaultProjectIds = [projectId]
  return (
    <>
      <Button
        type="button"
        {...button}
        // A failed provider list would look empty and send an org that has providers through
        // the add-a-provider wizard.
        disabled={!hasCompleteItems}
        title={providersQuery.isError ? 'Could not load model providers.' : undefined}
        onClick={() => {
          setStartWithProvider(providers.length === 0)
          setOpen(true)
        }}
      >
        {children}
      </Button>
      {startWithProvider ? (
        <CreateModelProviderDialog
          // Keyed by project: the page stays mounted when switching projects, and the dialog's
          // share defaults are only read when it mounts.
          key={projectId}
          open={open}
          onOpenChange={setOpen}
          orgId={orgId}
          defaultProjectIds={defaultProjectIds}
        />
      ) : (
        <CreateConfiguredModelDialog
          key={projectId}
          open={open}
          onOpenChange={setOpen}
          orgId={orgId}
          providers={providers}
          defaultProjectIds={defaultProjectIds}
        />
      )}
    </>
  )
}
