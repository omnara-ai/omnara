import { useRemoveOrgMember, useUpdateOrgMemberRole } from '@omnara/react'
import type { OrgMember } from '@omnara/sdk'

import { MemberProjectAccessSection } from '@/components/org/MemberProjectAccessSection'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
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
      <dl className="grid gap-y-3 text-sm sm:grid-cols-[max-content_minmax(0,1fr)] sm:gap-x-8 sm:gap-y-2">
        <div className="grid gap-1 sm:col-span-2 sm:grid-cols-subgrid sm:gap-0">
          <dt className="text-muted-foreground">User ID</dt>
          <dd className="break-all font-mono text-xs leading-5">{member.user_id}</dd>
        </div>
        <div className="grid gap-1 sm:col-span-2 sm:grid-cols-subgrid sm:gap-0">
          <dt className="text-muted-foreground">Joined</dt>
          <dd className="break-words">{formatDateTime(member.created_at)}</dd>
        </div>
      </dl>
      {canManage && (
        <>
          <Separator />
          <dl className="grid gap-y-3 text-sm sm:grid-cols-[max-content_minmax(0,1fr)] sm:gap-x-8 sm:gap-y-2">
            {canManageThisMember && (
              <div className="grid gap-1 sm:col-span-2 sm:grid-cols-subgrid sm:items-center sm:gap-0">
                <dt className="text-muted-foreground">Org role</dt>
                <dd className="flex min-w-0 flex-col gap-2">
                  <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3">
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
                      loading={removeMember.isPending}
                      onClick={() => {
                        if (
                          window.confirm(`Remove ${memberName(member)} from this organization?`)
                        ) {
                          removeMember.mutate(member.user_id)
                        }
                      }}
                    >
                      Remove from organization
                    </Button>
                  </div>
                  {removeError && (
                    <p role="alert" className="text-destructive text-xs sm:text-right">
                      {removeError}
                    </p>
                  )}
                </dd>
              </div>
            )}
            <div className="grid gap-1 sm:col-span-2 sm:grid-cols-subgrid sm:gap-0">
              <dt className="text-muted-foreground">Project access</dt>
              <dd className="min-w-0 break-words">
                <MemberProjectAccessSection orgId={orgId} member={member} />
              </dd>
            </div>
          </dl>
        </>
      )}
    </div>
  )
}
