import { useGrantSecretToProject, useProjectAvailableSecrets, useSecrets } from '@omnara/react'
import type { Secret } from '@omnara/sdk'
import { useState } from 'react'

import { GrantProjectResourcesDialog } from '@/components/projects/GrantProjectResourcesDialog'
import { Button } from '@/components/ui/button'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { canManageOrg } from '@/lib/permissions'
import { secretSubtitle } from '@/lib/secrets'
import { useActiveOrg } from '@/lib/use-active-org'

const SecretMultiCombobox = createResourceMultiCombobox<Secret>({
  itemKey: (secret) => secret.id,
  itemLabel: (secret) => secret.name,
  renderItem: (secret) => (
    <span className="flex min-w-0 flex-col">
      <span className="truncate">{secret.name}</span>
      <span className="text-muted-foreground text-xs">
        {secret.owner.kind === 'org' ? 'Organization' : 'Personal'} · {secretSubtitle(secret)}
      </span>
    </span>
  ),
  placeholder: 'Search secrets…',
  emptyMessage: 'No secrets to share.',
})

/**
 * "Share secrets" for a project's Shared tab: grants the viewer's personal secrets, and
 * organization secrets for org admins, to the project. The caller checks the viewer can
 * manage the project.
 */
export function GrantProjectSecretsButton({
  orgId,
  projectId,
}: {
  orgId: string
  projectId: string
}) {
  const [open, setOpen] = useState(false)
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
        Share secrets
      </Button>
      {open && (
        <GrantProjectSecretsDialog
          open={open}
          onOpenChange={setOpen}
          orgId={orgId}
          projectId={projectId}
        />
      )}
    </>
  )
}

function GrantProjectSecretsDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
}) {
  const { activeOrg } = useActiveOrg()
  const canShareOrgSecrets = canManageOrg(activeOrg.role)
  const grantSecret = useGrantSecretToProject(orgId)
  const search = useTypeaheadSearch()
  const secretsQuery = useSecrets(orgId, undefined, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const sharedQuery = useProjectAvailableSecrets(orgId, projectId, {
    filters: { availability_source: 'grant' },
    pageSize: 100,
  })
  const shared = useCompleteInfiniteQueryItems(sharedQuery, open)
  const sharedIds = new Set(shared.items.map((access) => access.secret.id))
  // Built-in (cluster-managed) secrets aren't shareable; neither are other projects' secrets.
  const secrets = useInfiniteQueryItems(secretsQuery).filter(
    (secret) =>
      secret.management_kind === 'tenant' &&
      (secret.owner.kind === 'user' || (secret.owner.kind === 'org' && canShareOrgSecrets)) &&
      !sharedIds.has(secret.id),
  )

  return (
    <GrantProjectResourcesDialog
      open={open}
      onOpenChange={onOpenChange}
      noun="secret"
      description="Let agents in this project use one of your secrets."
      combobox={SecretMultiCombobox}
      itemKey={(secret) => secret.id}
      items={secrets}
      search={search}
      query={secretsQuery}
      pending={secretsQuery.isPending || !shared.isComplete}
      isError={secretsQuery.isError || sharedQuery.isError}
      onRetry={() => {
        void Promise.all([secretsQuery.refetch(), sharedQuery.refetch()])
      }}
      emptyHint={
        canShareOrgSecrets
          ? 'Every personal and organization secret is already shared, or none exist yet.'
          : 'Every personal secret is already shared, or you have none yet.'
      }
      grant={async (secret) => {
        await grantSecret.mutateAsync({ secretID: secret.id, projectID: projectId })
      }}
    />
  )
}
