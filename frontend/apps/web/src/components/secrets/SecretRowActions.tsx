import { useDeleteSecret, useDeleteSecretGrant, useGrantSecretToProject } from '@omnara/react'
import { ApiError, type ProjectSecretAccess, type Secret } from '@omnara/sdk'
import { Ellipsis } from 'lucide-react'
import { useState } from 'react'

import { EditSecretDialog } from '@/components/org/EditSecretDialog'
import { GrantToProjectDialog } from '@/components/projects/GrantToProjectDialog'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export function SecretRowActions({
  orgId,
  secret,
  availability,
  projectName,
  canDelete,
  canEdit = false,
  canGrant = false,
}: {
  orgId: string
  secret: Secret
  availability?: ProjectSecretAccess['availability']
  projectName?: string
  canDelete: boolean
  canEdit?: boolean
  canGrant?: boolean
}) {
  const deleteSecretMutation = useDeleteSecret(orgId)
  const deleteSecretGrantMutation = useDeleteSecretGrant(orgId)
  const grantSecretMutation = useGrantSecretToProject(orgId)
  const [editOpen, setEditOpen] = useState(false)
  const [grantOpen, setGrantOpen] = useState(false)
  const mcpUrl = secret.metadata.mcp_url
  const canCopyMcpConfig = mcpUrl !== undefined && mcpUrl !== ''
  const isGrant = availability?.source === 'grant'
  const tenantManaged = secret.management_kind === 'tenant'
  const allowDelete = canDelete && tenantManaged
  const allowEdit = canEdit && tenantManaged
  const allowGrant = canGrant && tenantManaged

  if (!allowDelete && !canCopyMcpConfig && !allowEdit && !allowGrant) {
    return null
  }

  async function copyMcpConfig() {
    if (mcpUrl === undefined || mcpUrl === '') {
      return
    }
    try {
      await navigator.clipboard.writeText(
        [
          'mcp:',
          '  server:',
          `    url: ${JSON.stringify(mcpUrl)}`,
          '    auth:',
          '      type: oauth',
          `      secret_id: ${JSON.stringify(secret.id)}`,
        ].join('\n'),
      )
    } catch {
      window.alert('Could not copy MCP config')
    }
  }

  async function deleteSecret() {
    const message = isGrant ? 'Remove this secret grant?' : 'Delete this secret?'
    if (!window.confirm(message)) {
      return
    }
    try {
      if (availability?.source === 'grant') {
        await deleteSecretGrantMutation.mutateAsync({
          secretID: secret.id,
          grantID: availability.grant_id,
        })
      } else {
        await deleteSecretMutation.mutateAsync(secret.id)
      }
    } catch (err) {
      window.alert(err instanceof ApiError ? err.message : 'Could not delete secret')
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" aria-label="Secret actions">
            <Ellipsis />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {allowEdit && (
            <DropdownMenuItem
              onSelect={() => {
                setEditOpen(true)
              }}
            >
              Edit
            </DropdownMenuItem>
          )}
          {allowGrant && (
            <DropdownMenuItem
              onSelect={() => {
                setGrantOpen(true)
              }}
            >
              Grant to project
            </DropdownMenuItem>
          )}
          {canCopyMcpConfig && (
            <DropdownMenuItem
              onSelect={() => {
                void copyMcpConfig()
              }}
            >
              Copy MCP config
            </DropdownMenuItem>
          )}
          {allowDelete && (
            <DropdownMenuItem
              variant="destructive"
              className={isGrant ? 'items-start' : undefined}
              onSelect={() => {
                void deleteSecret()
              }}
            >
              {isGrant ? (
                <span className="flex flex-col gap-0.5">
                  <span>Remove secret grant</span>
                  <span className="text-muted-foreground text-xs font-normal">
                    Removes {projectName ?? 'this project'}&rsquo;s access to this secret
                  </span>
                </span>
              ) : (
                'Delete'
              )}
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {/* Mounted only while open so the dialogs seed fresh state each time. */}
      {allowEdit && editOpen && (
        <EditSecretDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setEditOpen(false)
          }}
          orgId={orgId}
          secret={secret}
        />
      )}
      {allowGrant && grantOpen && (
        <GrantToProjectDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setGrantOpen(false)
          }}
          orgId={orgId}
          resourceName={secret.name}
          isProjectEligible={(project) => project.access.can_manage}
          excludedProjectIds={secret.owner.kind === 'project' ? [secret.owner.project_id] : []}
          onGrant={(projectID) =>
            grantSecretMutation.mutateAsync({ projectID, secretID: secret.id })
          }
        />
      )}
    </>
  )
}
