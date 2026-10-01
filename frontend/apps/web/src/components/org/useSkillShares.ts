import { useGrantSkillToProject } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { useState } from 'react'

interface SkillGrant {
  skillID: string
  projectID: string
}

/**
 * Projects to share newly created skills with, and the shares that still need a retry
 * after some of them failed.
 */
export function useSkillShares(orgId: string) {
  const grantSkill = useGrantSkillToProject(orgId)
  const [projectIds, setProjectIds] = useState<string[]>([])
  const [pending, setPending] = useState<SkillGrant[]>([])
  const [sharing, setSharing] = useState(false)
  const [error, setError] = useState<string>()

  async function grant(grants: SkillGrant[]) {
    if (grants.length === 0) return true
    setSharing(true)
    setError(undefined)
    try {
      const results = await Promise.allSettled(grants.map((each) => grantSkill.mutateAsync(each)))
      const failed = grants.filter((_, index) => results[index]?.status === 'rejected')
      setPending(failed)
      if (failed.length > 0) {
        const projects = new Set(failed.map((each) => each.projectID)).size
        setError(
          `The skill was created, but sharing with ${String(projects)} ${projects === 1 ? 'project' : 'projects'} failed.`,
        )
      }
      return failed.length === 0
    } finally {
      setSharing(false)
    }
  }

  return {
    projectIds,
    setProjectIds,
    pending,
    sharing,
    error,
    /** Shares each created skill with the chosen projects; true when every share succeeded. */
    share: (skills: Skill[]) =>
      grant(
        skills.flatMap((skill) =>
          projectIds.map((projectID) => ({ skillID: skill.id, projectID })),
        ),
      ),
    retry: () => grant(pending),
    reset: () => {
      setProjectIds([])
      setPending([])
      setError(undefined)
    },
  }
}
