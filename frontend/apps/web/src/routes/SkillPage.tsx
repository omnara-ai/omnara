import { useSkill } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SkillView } from '@/components/skills/SkillView'
import { Button } from '@/components/ui/button'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function SkillPage() {
  const { activeOrg } = useActiveOrg()
  const { skillId = '' } = useParams({ strict: false })
  const navigate = useNavigate()
  const skill = useSkill(activeOrg.id, skillId).data
  const listSearch = skill?.owner.kind === 'org' ? { owner: 'organization' as const } : undefined

  // User skills are only visible to their owner; org skills need org management to change.
  function permissions(skill: Skill) {
    const canManage =
      skill.owner.kind === 'user' || (skill.owner.kind === 'org' && canManageOrg(activeOrg.role))
    return { canManage, canGrant: canManage }
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'skills', label: 'Skills', to: '/skills', search: listSearch },
          { id: 'skill', label: skill?.name ?? 'Skill' },
        ]}
      />
      <SkillView
        orgId={activeOrg.id}
        skillId={skillId}
        permissions={permissions}
        backLink={
          <Button asChild size="sm" variant="ghost">
            <Link to="/skills">Back to skills</Link>
          </Button>
        }
        onRemoved={() => {
          void navigate({ to: '/skills', search: listSearch })
        }}
      />
    </div>
  )
}
