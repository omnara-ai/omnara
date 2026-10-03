import { useGrantSkillToProject } from '@omnara/react'
import type { Skill } from '@omnara/sdk'

import { useProjectShares } from '@/hooks/use-project-shares'

/**
 * Projects to share newly created skills with, and the shares that still need a retry
 * after some of them failed.
 */
export function useSkillShares(orgId: string) {
  const grantSkill = useGrantSkillToProject(orgId)
  const shares = useProjectShares(async ({ resourceId, projectId }) => {
    await grantSkill.mutateAsync({ skillID: resourceId, projectID: projectId })
  })
  return {
    ...shares,
    error:
      shares.error &&
      `${shares.unfinishedCount === 1 ? 'The skill was' : 'The skills were'} created. ${shares.error}`,
    /** Shares each created skill with the chosen projects; true when every share succeeded. */
    share: (skills: Skill[]) => shares.share(skills.map((skill) => skill.id)),
  }
}
