import {
  useOrgApiKeyProjectAccess,
  useRemoveOrgApiKeyProjectRole,
  useRevokeOrgApiKey,
  useSetOrgApiKeyProjectRole,
  useUpdateOrgApiKey,
} from '@omnara/react'
import type { OrgApiKey } from '@omnara/sdk'

import { ProjectAccessEditor } from '@/components/org/ProjectAccessEditor'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { errorMessage } from '@/lib/submit-status'

const ORG_ROLES = ['member', 'admin'] as const

export function OrgApiKeyDetailPanel({ orgId, apiKey }: { orgId: string; apiKey: OrgApiKey }) {
  const updateKey = useUpdateOrgApiKey(orgId)
  const revokeKey = useRevokeOrgApiKey(orgId)
  const isRevoked = apiKey.revoked_at !== null
  const isImplicitProjectAdmin = apiKey.org_role === 'admin'
  const accessQuery = useOrgApiKeyProjectAccess(orgId, apiKey.id, {
    enabled: !isRevoked && !isImplicitProjectAdmin,
  })
  const setAccess = useSetOrgApiKeyProjectRole(orgId, apiKey.id)
  const removeAccess = useRemoveOrgApiKeyProjectRole(orgId, apiKey.id)
  const updateError = updateKey.isError
    ? errorMessage(updateKey.error, 'Could not update this API token’s organization role.')
    : ''
  const revokeError = revokeKey.isError
    ? errorMessage(revokeKey.error, 'Could not revoke this API token.')
    : ''

  return (
    <div className="flex flex-col gap-3">
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-8 gap-y-2 text-sm">
        <div className="col-span-2 grid grid-cols-subgrid">
          <dt className="text-muted-foreground">Key ID</dt>
          <dd className="break-all font-mono text-xs leading-5">{apiKey.id}</dd>
        </div>
        <div className="col-span-2 grid grid-cols-subgrid">
          <dt className="text-muted-foreground">Created by</dt>
          <dd className="break-all font-mono text-xs leading-5">{apiKey.created_by_user_id}</dd>
        </div>
      </dl>
      {!isRevoked && (
        <>
          <Separator />
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-8 gap-y-2 text-sm">
            <div className="col-span-2 grid grid-cols-subgrid items-center">
              <dt className="text-muted-foreground">Org role</dt>
              <dd className="flex flex-col gap-2">
                <div className="flex items-center justify-between gap-3">
                  <div className="flex gap-2">
                    {ORG_ROLES.map((role) => (
                      <Button
                        key={role}
                        type="button"
                        size="sm"
                        variant={apiKey.org_role === role ? 'default' : 'outline'}
                        className="capitalize"
                        disabled={updateKey.isPending}
                        onClick={() => {
                          if (apiKey.org_role !== role) {
                            updateKey.mutate({ keyID: apiKey.id, org_role: role })
                          }
                        }}
                      >
                        {role}
                      </Button>
                    ))}
                  </div>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="text-destructive hover:text-destructive shrink-0"
                    disabled={revokeKey.isPending}
                    loading={revokeKey.isPending}
                    onClick={() => {
                      if (
                        window.confirm(
                          `Revoke the API token “${apiKey.name}”? This cannot be undone.`,
                        )
                      ) {
                        revokeKey.mutate(apiKey.id)
                      }
                    }}
                  >
                    Revoke token
                  </Button>
                </div>
                {updateError && (
                  <p role="alert" className="text-destructive text-right text-xs">
                    {updateError}
                  </p>
                )}
                {revokeError && (
                  <p role="alert" className="text-destructive text-right text-xs">
                    {revokeError}
                  </p>
                )}
              </dd>
            </div>
            <div className="col-span-2 grid grid-cols-subgrid">
              <dt className="text-muted-foreground">Project access</dt>
              <dd className="break-words">
                {isImplicitProjectAdmin ? (
                  <p className="text-muted-foreground">
                    Admin tokens have admin access to every project.
                  </p>
                ) : (
                  <ProjectAccessEditor
                    orgId={orgId}
                    accessQuery={accessQuery}
                    setAccess={setAccess}
                    removeAccess={removeAccess}
                  />
                )}
              </dd>
            </div>
          </dl>
        </>
      )}
    </div>
  )
}
