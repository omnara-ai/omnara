import { useRemoveOrgMember, useUpdateOrgMemberRole } from '@omnara/react'
import type { OrgMember } from '@omnara/sdk'

import { MemberProjectAccessSection } from '@/components/org/MemberProjectAccessSection'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { Spinner } from '@/components/ui/spinner'
import { formatDateTime } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'

const ORG_ROLES = ['member', 'admin'] as const

function memberName(member: OrgMember) {
  return member.display_name || member.email || 'Unnamed user'
}

export function MemberDetailPanel({ orgId, member }: { orgId: string; member: OrgMember }) {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const updateRole = useUpdateOrgMemberRole(orgId)
  const removeMember = useRemoveOrgMember(orgId)
  const isOwner = member.role === 'owner'
  const canManageThisMember = canManage && !isOwner
  const removeError = removeMember.isError
    ? errorMessage(removeMember.error, 'Could not remove this member from the organization.')
    : ''

  return (
    <div className="flex flex-col gap-3">
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-8 gap-y-2 text-sm">
        <div className="col-span-2 grid grid-cols-subgrid">
          <dt className="text-muted-foreground">User ID</dt>
          <dd className="break-all font-mono text-xs leading-5">{member.user_id}</dd>
        </div>
        <div className="col-span-2 grid grid-cols-subgrid">
          <dt className="text-muted-foreground">Joined</dt>
          <dd className="break-words">{formatDateTime(member.created_at)}</dd>
        </div>
      </dl>
      {canManage && (
        <>
          <Separator />
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-8 gap-y-2 text-sm">
            {canManageThisMember && (
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
                          variant={member.role === role ? 'default' : 'outline'}
                          className="capitalize"
                          disabled={updateRole.isPending}
                          onClick={() => {
                            if (member.role !== role) {
                              updateRole.mutate({ userID: member.user_id, role })
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
                      disabled={removeMember.isPending}
                      onClick={() => {
                        if (
                          window.confirm(`Remove ${memberName(member)} from this organization?`)
                        ) {
                          removeMember.mutate(member.user_id)
                        }
                      }}
                    >
                      {removeMember.isPending && <Spinner />}
                      Remove from organization
                    </Button>
                  </div>
                  {removeError && (
                    <p role="alert" className="text-destructive text-right text-xs">
                      {removeError}
                    </p>
                  )}
                </dd>
              </div>
            )}
            <div className="col-span-2 grid grid-cols-subgrid">
              <dt className="text-muted-foreground">Project access</dt>
              <dd className="break-words">
                <MemberProjectAccessSection orgId={orgId} member={member} />
              </dd>
            </div>
          </dl>
        </>
      )}
    </div>
  )
}
