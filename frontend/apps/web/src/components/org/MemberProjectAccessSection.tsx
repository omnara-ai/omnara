import {
  useMemberProjectAccess,
  useRemoveMemberProjectAccess,
  useSetMemberProjectAccess,
} from '@omnara/react'
import type { OrgMember } from '@omnara/sdk'

import { ProjectAccessEditor } from '@/components/org/ProjectAccessEditor'

export function MemberProjectAccessSection({
  orgId,
  member,
}: {
  orgId: string
  member: OrgMember
}) {
  const isImplicitProjectAdmin = member.role === 'owner' || member.role === 'admin'
  const accessQuery = useMemberProjectAccess(orgId, member.user_id, {
    enabled: !isImplicitProjectAdmin,
  })
  const setAccess = useSetMemberProjectAccess(orgId, member.user_id)
  const removeAccess = useRemoveMemberProjectAccess(orgId, member.user_id)

  if (isImplicitProjectAdmin) {
    return (
      <p className="text-muted-foreground">
        {member.role === 'owner' ? 'Owners' : 'Admins'} have admin access to every project.
      </p>
    )
  }

  return (
    <ProjectAccessEditor
      orgId={orgId}
      accessQuery={accessQuery}
      setAccess={setAccess}
      removeAccess={removeAccess}
    />
  )
}
