import { useSkill } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'

import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { SkillView } from '@/components/skills/SkillView'
import { Button } from '@/components/ui/button'
import { useActiveOrg } from '@/lib/use-active-org'

export function ProjectSkillPage() {
  const { projectId = '', skillId = '' } = useParams({ strict: false })
  const navigate = useNavigate()
  const { activeOrg } = useActiveOrg()
  const skill = useSkill(activeOrg.id, skillId).data
  const listSearch =
    skill && !(skill.owner.kind === 'project' && skill.owner.project_id === projectId)
      ? { tab: 'shared' as const }
      : {}

  return (
    <ProjectPageFrame
      crumbs={[
        {
          id: 'skills',
          label: 'Skills',
          to: '/projects/$projectId/skills',
          params: { projectId },
          search: listSearch,
        },
        { id: 'skill', label: skill?.name ?? 'Skill' },
      ]}
    >
      {({ project }) => {
        // Only this project's own skills are managed here; shared skills are changed by
        // their owner, and stop-sharing lives on the Shared tab.
        const permissions = (skill: Skill) => ({
          canManage:
            skill.owner.kind === 'project' &&
            skill.owner.project_id === projectId &&
            (project?.access.can_manage ?? false),
          canGrant: false,
        })
        return (
          <SkillView
            orgId={activeOrg.id}
            skillId={skillId}
            permissions={permissions}
            backLink={
              <Button asChild size="sm" variant="ghost">
                <Link to="/projects/$projectId/skills" params={{ projectId }}>
                  Back to skills
                </Link>
              </Button>
            }
            onRemoved={() => {
              void navigate({
                to: '/projects/$projectId/skills',
                params: { projectId },
                search: listSearch,
              })
            }}
          />
        )
      }}
    </ProjectPageFrame>
  )
}
